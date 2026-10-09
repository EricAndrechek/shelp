package provider

// Builds, converts, and signs shelp's one Apple Shortcuts "Use Model"
// shortcut. Since format 5 a single shortcut serves every shortcuts:*
// provider: its input names the model (see buildWorkflow).
//
// The plist has no WFWorkflowName field, so the shortcut's display name
// comes from the *filename* of the .shortcut file at import time.
// GenerateShortcut therefore writes "<name>.shortcut" verbatim.
//
// Pipeline: build JSON in Go -> `plutil -convert binary1` (JSON -> binary
// plist) -> `shortcuts sign --mode anyone` (binary plist -> importable
// .shortcut). Both steps are silent CLI operations; nothing pops a dialog
// until the resulting file is opened (which only the setup step, deliberately,
// ever does).
//
// Every action shape below is taken from a real, stored action, never
// guessed. Sources, cited per type: the live-verified format 4 generator
// (which itself came from Gallery.bundle's Haiku.wflow and the If actions
// in Eric's library); read-only copies of Eric's Shortcuts library
// (~/Library/Shortcuts/Shortcuts.sqlite, ZSHORTCUTACTIONS.ZDATA, macOS 27);
// and github.com/viticci/shortcuts-playground-plugin's CONTROL_FLOW.md and
// EXAMPLES.md (macOS 27, checked against Apple-built samples).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// wfIcon is WFWorkflowIcon.
type wfIcon struct {
	WFWorkflowIconStartColor  int `json:"WFWorkflowIconStartColor"`
	WFWorkflowIconGlyphNumber int `json:"WFWorkflowIconGlyphNumber"`
}

// wfAttachment is one entry of a WFTextTokenString's attachmentsByRange, or
// (wrapped in wfTextTokenAttachment) a directly-referenced single value.
// Shortcut Input is {"Type": "ExtensionInput"}; an earlier action's output
// is {"Type": "ActionOutput", "OutputUUID": <its UUID>, "OutputName":
// <its output's name>}.
type wfAttachment struct {
	Type       string `json:"Type"`
	OutputUUID string `json:"OutputUUID,omitempty"`
	OutputName string `json:"OutputName,omitempty"`
}

// Output names of the actions buildWorkflow references, as stored in real
// libraries: a Text action's is "Text" (the v4 If; Wolfram Alpha 2
// Clipboard and XML To Dictionary), Get Dictionary from Input's is
// "Dictionary" and Get Dictionary Value's is "Dictionary Value" (both in
// Wolfram Alpha 2 Clipboard), Use Model's is "Response" (the v4 shortcut;
// Gallery Haiku.wflow).
const (
	outputNameText            = "Text"
	outputNameDictionary      = "Dictionary"
	outputNameDictionaryValue = "Dictionary Value"
	outputNameResponse        = "Response"
)

func actionOutput(uuid, name string) wfAttachment {
	return wfAttachment{Type: "ActionOutput", OutputUUID: uuid, OutputName: name}
}

// wfTextTokenValue is a WFTextTokenString's Value.
type wfTextTokenValue struct {
	String             string                  `json:"string"`
	AttachmentsByRange map[string]wfAttachment `json:"attachmentsByRange"`
}

// wfTextToken is a WFTextTokenString-typed parameter value (WFLLMPrompt,
// WFOutput, WFTextActionText).
type wfTextToken struct {
	WFSerializationType string           `json:"WFSerializationType"`
	Value               wfTextTokenValue `json:"Value"`
}

// literalText is a WFTextTokenString with no attachments.
func literalText(s string) wfTextToken {
	return wfTextToken{
		WFSerializationType: "WFTextTokenString",
		Value:               wfTextTokenValue{String: s, AttachmentsByRange: map[string]wfAttachment{}},
	}
}

// textWith is a WFTextTokenString reading prefix followed by att. The
// attachment's key is "{<offset>, 1}", the offset of its U+FFFC placeholder
// in UTF-16 code units: Wolfram Alpha 2 Clipboard's Text action stores
// "https://api.wolframalpha.com/v2/query?input=￼&..." with its
// attachment at "{44, 1}", the placeholder's index.
func textWith(prefix string, att wfAttachment) wfTextToken {
	off := len(utf16.Encode([]rune(prefix)))
	return wfTextToken{
		WFSerializationType: "WFTextTokenString",
		Value: wfTextTokenValue{
			String:             prefix + extensionInputPlaceholder,
			AttachmentsByRange: map[string]wfAttachment{fmt.Sprintf("{%d, 1}", off): att},
		},
	}
}

// wfTextTokenAttachment is a parameter value that references a single
// attachment directly (Value is the attachment dict itself), rather than
// interpolating it into a string like wfTextToken does. Get Dictionary from
// Input and Get Dictionary Value take their WFInput this way (Wolfram Alpha
// 2 Clipboard, XML To Dictionary).
type wfTextTokenAttachment struct {
	WFSerializationType string       `json:"WFSerializationType"`
	Value               wfAttachment `json:"Value"`
}

func tokenAttachment(att wfAttachment) wfTextTokenAttachment {
	return wfTextTokenAttachment{WFSerializationType: "WFTextTokenAttachment", Value: att}
}

// wfConditionInput is the If action's WFInput: unlike WFFile and the like,
// it wraps the WFTextTokenAttachment in a {"Type": "Variable", "Variable":
// ...} envelope. Every If action stored in a real Shortcuts library has
// this shape (checked against 20 on macOS 27). Format 2 put the bare
// attachment here instead.
type wfConditionInput struct {
	Type     string                `json:"Type"`
	Variable wfTextTokenAttachment `json:"Variable"`
}

func conditionInput(att wfAttachment) *wfConditionInput {
	return &wfConditionInput{Type: "Variable", Variable: tokenAttachment(att)}
}

// askLLMParams is WFWorkflowActionParameters for is.workflow.actions.askllm.
// Its WFLLMPrompt is a WFTextTokenString; format 4 attached Shortcut Input
// there, format 5 attaches the "prompt" Dictionary Value -- an ActionOutput
// in a prompt, as viticci's EXAMPLES.md "AI Query" (an Ask for Input's
// "Provided Input") and VARIABLES.md (a Text action's "Text") store it.
type askLLMParams struct {
	UUID                   string      `json:"UUID"`
	WFLLMModel             string      `json:"WFLLMModel"`
	WFGenerativeResultType string      `json:"WFGenerativeResultType"`
	FollowUp               bool        `json:"FollowUp"`
	WFAllowWebSearch       bool        `json:"WFAllowWebSearch"`
	WFLLMPrompt            wfTextToken `json:"WFLLMPrompt"`
}

// textParams is WFWorkflowActionParameters for is.workflow.actions.gettext
// (the "Text" action), whose output is named "Text". Shape as stored in a
// real Shortcuts library on macOS 27.
type textParams struct {
	UUID             string      `json:"UUID"`
	WFTextActionText wfTextToken `json:"WFTextActionText"`
}

// detectDictionaryParams is WFWorkflowActionParameters for
// is.workflow.actions.detect.dictionary ("Get Dictionary from Input"),
// output "Dictionary". Stored shape (XML To Dictionary, action 8, run on a
// "Text" output; Wolfram Alpha 2 Clipboard, action 5):
//
//	{"UUID": ..., "WFInput": {"Value": {"OutputUUID": ..., "Type":
//	"ActionOutput", "OutputName": "Text"}, "WFSerializationType":
//	"WFTextTokenAttachment"}}
type detectDictionaryParams struct {
	UUID    string                `json:"UUID"`
	WFInput wfTextTokenAttachment `json:"WFInput"`
}

// getValueForKeyParams is WFWorkflowActionParameters for
// is.workflow.actions.getvalueforkey ("Get Dictionary Value"), output
// "Dictionary Value". Stored shape (Wolfram Alpha 2 Clipboard, actions
// 6-10; none in the library sets WFGetDictionaryValueType, whose default
// is "Value"):
//
//	{"UUID": ..., "WFDictionaryKey": "queryresult", "WFInput": {"Value":
//	{"OutputUUID": ..., "Type": "ActionOutput", "OutputName":
//	"Dictionary"}, "WFSerializationType": "WFTextTokenAttachment"}}
type getValueForKeyParams struct {
	UUID            string                `json:"UUID"`
	WFDictionaryKey string                `json:"WFDictionaryKey"`
	WFInput         wfTextTokenAttachment `json:"WFInput"`
}

// outputParams is WFWorkflowActionParameters for is.workflow.actions.output
// ("Stop and Output").
type outputParams struct {
	WFOutput wfTextToken `json:"WFOutput"`
}

// conditionalParams is WFWorkflowActionParameters for
// is.workflow.actions.conditional (an "If"/"Otherwise"/"End If" triplet).
// All three actions in one group share GroupingIdentifier and repeat the
// same action identifier; WFControlFlowMode distinguishes which part of the
// group a given entry is (see the WFControlFlowMode* constants below). Only
// the "If" entry sets WFCondition/WFConditionalActionString/WFInput; the
// "End If" entries here carry a UUID, as format 4's did (live-verified; real
// libraries store End Ifs both with and without one -- PDFtoolkit has both).
//
// No macOS 27 Gallery.bundle *.wflow contains a conditional action, so the
// shape comes from the If actions stored in a real Shortcuts library
// (~/Library/Shortcuts/Shortcuts.sqlite, ZSHORTCUTACTIONS.ZDATA, read from
// a copy): WFCondition 4 with a literal WFConditionalActionString is how
// "<text> is <string>" is stored, and WFInput is a wfConditionInput.
// DocumentReview.wflow's repeat.count/choosefrommenu actions confirm the
// shared WFControlFlowMode 0/1/2 + GroupingIdentifier scaffold.
type conditionalParams struct {
	GroupingIdentifier        string            `json:"GroupingIdentifier"`
	WFControlFlowMode         int               `json:"WFControlFlowMode"`
	UUID                      string            `json:"UUID,omitempty"`
	WFCondition               int               `json:"WFCondition,omitempty"`
	WFConditionalActionString string            `json:"WFConditionalActionString,omitempty"`
	WFInput                   *wfConditionInput `json:"WFInput,omitempty"`
}

// WFControlFlowMode values for a control-flow action group (conditional,
// repeat, choosefrommenu, ...): the first entry opens the group, an
// optional middle entry marks a branch/case boundary, and the last entry
// closes it. Verified against macOS 27's Gallery.bundle/DocumentReview.wflow
// (is.workflow.actions.repeat.count and .choosefrommenu both use 0/1/2 this
// way) and against Cherri's shortcutgen.go (startStatement/statementPart/
// endStatement uint64 constants = 0/1/2).
const (
	wfControlFlowStart  = 0 // "If"
	wfControlFlowMiddle = 1 // "Otherwise"
	wfControlFlowEnd    = 2 // "End If"
)

// conditionIsEquals is the WFCondition code for "Is" (equals) on a text
// input: a real library's "If Type is Safari web page" stores WFCondition
// 4 with WFConditionalActionString "Safari web page", and
// github.com/electrikmilk/cherri's `conditions` map and viticci's
// CONTROL_FLOW.md ("4 | is (string equals)") agree.
const conditionIsEquals = 4

// ShortcutFormatVersion is the current generated shortcut's plist format.
// It increments whenever buildWorkflow's action shape changes in a way
// that requires an installed shortcut to be regenerated (see shortcuts.go's
// outdated-version detection). 1 was the original marker-less, 2-action
// (Use Model + Stop and Output) shape. 2 added the version-marker If block
// below, and 3 fixed its WFInput wrapper, but both compared Shortcut Input
// itself, and a shortcut that takes text or files has no text type for
// "is" to compare: Shortcuts drops the comparison string, and every run,
// real prompts included, fails at the If with "Please choose a value for
// each parameter in this action." (live on macOS 27, 2026-10-07). 4
// compares the output of a Text action holding Shortcut Input instead, the
// way Apple-built shortcuts do text comparisons. 1-4 were one shortcut per
// (model, web) pair, named "shelp · <Model>[ + Web]". 5 is one shortcut
// for all of them, its input a JSON {"model", "prompt"} object.
const ShortcutFormatVersion = 5

// shortcutVersionMarker is the literal Shortcut Input value that asks a
// generated shortcut to report its own format version instead of calling
// Use Model. buildWorkflow wires it into a leading "If Shortcut Input is
// <marker>" action, so this check never reaches Apple's model backend --
// see CLAUDE.md's "Shortcuts Use Model" section and shortcuts.go's
// detection code, which relies on that guarantee.
const shortcutVersionMarker = "__shelp_shortcut_version__"

// shortcutVersionOutputText is what a >=2 shortcut answers when its
// Shortcut Input is exactly shortcutVersionMarker.
func shortcutVersionOutputText(version int) string {
	return fmt.Sprintf("shelp-shortcut %d", version)
}

// shortcutErrorPrefix starts the text a format 5+ shortcut answers when
// its input names no model it knows ("shelp-shortcut-error: unknown model
// <key>"): its last Otherwise, reached without calling Use Model.
const shortcutErrorPrefix = "shelp-shortcut-error:"

// shortcutUnknownModelText is the fallback branch's literal text; the
// model value follows it.
const shortcutUnknownModelText = shortcutErrorPrefix + " unknown model "

// shortcutRequest is a format 5+ shortcut's input: the JSON object its Get
// Dictionary from Input parses. Model is a shortcutVariant key, Prompt the
// whole Use Model prompt.
type shortcutRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// wfAction is one entry of WFWorkflowActions.
type wfAction struct {
	WFWorkflowActionIdentifier string `json:"WFWorkflowActionIdentifier"`
	WFWorkflowActionParameters any    `json:"WFWorkflowActionParameters"`
}

// workflow is the unsigned .shortcut plist, as JSON. Field order here
// doesn't matter: plutil/Shortcuts treat it as an unordered dictionary.
type workflow struct {
	WFWorkflowMinimumClientVersionString string     `json:"WFWorkflowMinimumClientVersionString"`
	WFWorkflowMinimumClientVersion       int        `json:"WFWorkflowMinimumClientVersion"`
	WFWorkflowClientVersion              string     `json:"WFWorkflowClientVersion"`
	WFWorkflowIcon                       wfIcon     `json:"WFWorkflowIcon"`
	WFWorkflowActions                    []wfAction `json:"WFWorkflowActions"`
	WFWorkflowHasShortcutInputVariables  bool       `json:"WFWorkflowHasShortcutInputVariables"`
	WFWorkflowInputContentItemClasses    []string   `json:"WFWorkflowInputContentItemClasses"`
	WFWorkflowOutputContentItemClasses   []string   `json:"WFWorkflowOutputContentItemClasses"`
	WFWorkflowHasOutputFallback          bool       `json:"WFWorkflowHasOutputFallback"`
	WFWorkflowTypes                      []string   `json:"WFWorkflowTypes"`
	WFQuickActionSurfaces                []string   `json:"WFQuickActionSurfaces"`
	WFWorkflowImportQuestions            []string   `json:"WFWorkflowImportQuestions"`
}

// extensionInputPlaceholder is U+FFFC OBJECT REPLACEMENT CHARACTER, the
// single character Shortcuts substitutes with an attachment (here, the
// Shortcut Input / previous action's output).
const extensionInputPlaceholder = "￼"

// wfBuilder accumulates WFWorkflowActions.
type wfBuilder struct{ actions []wfAction }

func (b *wfBuilder) add(ident string, params any) {
	b.actions = append(b.actions, wfAction{WFWorkflowActionIdentifier: "is.workflow.actions." + ident, WFWorkflowActionParameters: params})
}

// text adds a Text action holding att and returns its UUID.
func (b *wfBuilder) text(att wfAttachment) string {
	id := newUUID()
	b.add("gettext", textParams{UUID: id, WFTextActionText: textWith("", att)})
	return id
}

// ifTextIs opens an "If <Text output of textUUID> is <literal>" group and
// returns its GroupingIdentifier.
func (b *wfBuilder) ifTextIs(textUUID, literal string) string {
	g := newUUID()
	b.add("conditional", conditionalParams{
		GroupingIdentifier:        g,
		WFControlFlowMode:         wfControlFlowStart,
		WFCondition:               conditionIsEquals,
		WFConditionalActionString: literal,
		WFInput:                   conditionInput(actionOutput(textUUID, outputNameText)),
	})
	return g
}

func (b *wfBuilder) otherwise(group string) {
	b.add("conditional", conditionalParams{GroupingIdentifier: group, WFControlFlowMode: wfControlFlowMiddle})
}

func (b *wfBuilder) endIf(group string) {
	b.add("conditional", conditionalParams{GroupingIdentifier: group, WFControlFlowMode: wfControlFlowEnd, UUID: newUUID()})
}

func (b *wfBuilder) output(t wfTextToken) {
	b.add("output", outputParams{WFOutput: t})
}

// buildWorkflow returns the unsigned plist (as Go structs, ready to
// marshal to JSON) of shelp's one shortcut. With K the variant keys of
// shortcutVariants:
//
//	Text: Shortcut Input                                        [T]
//	If T is "__shelp_shortcut_version__"
//	    Stop and Output "shelp-shortcut 5"         (no model call)
//	Otherwise
//	    Get Dictionary from Input: T                            [D]
//	    Get Dictionary Value "model" in D                       [M]
//	    Get Dictionary Value "prompt" in D                      [P]
//	    Text: M                                                 [K]
//	    If K is K[0]
//	        Use Model (K[0]'s model and web flag), prompt P     [R0]
//	        Stop and Output R0
//	    Otherwise
//	        If K is K[1]
//	            ...
//	        Otherwise
//	            Stop and Output "shelp-shortcut-error: unknown model <K>"
//	        End If
//	    End If
//	End If
//
// The marker block is format 4's, unchanged: an If needs a text-typed
// subject, which Shortcut Input isn't, so it compares a Text action's
// output; the model value goes through a Text action for the same reason.
// The dispatch nests each If in the previous Otherwise, the shape real
// shortcuts store (PDFtoolkit's three nested "If ... contains" groups
// close innermost first, End If, End If, End If); macOS 27's "Otherwise
// If" (a mode-1 conditional with condition fields, per viticci's
// CONTROL_FLOW.md) appears in no library shortcut, so it isn't used. Each
// branch ends in its own Stop and Output, so nothing after a Use Model
// runs either way.
func buildWorkflow() workflow {
	var b wfBuilder

	inputText := b.text(wfAttachment{Type: "ExtensionInput"})
	marker := b.ifTextIs(inputText, shortcutVersionMarker)
	b.output(literalText(shortcutVersionOutputText(ShortcutFormatVersion)))
	b.otherwise(marker)

	dict := newUUID()
	b.add("detect.dictionary", detectDictionaryParams{UUID: dict, WFInput: tokenAttachment(actionOutput(inputText, outputNameText))})
	modelValue, promptValue := newUUID(), newUUID()
	b.add("getvalueforkey", getValueForKeyParams{UUID: modelValue, WFDictionaryKey: "model", WFInput: tokenAttachment(actionOutput(dict, outputNameDictionary))})
	b.add("getvalueforkey", getValueForKeyParams{UUID: promptValue, WFDictionaryKey: "prompt", WFInput: tokenAttachment(actionOutput(dict, outputNameDictionary))})
	modelText := b.text(actionOutput(modelValue, outputNameDictionaryValue))

	groups := make([]string, 0, len(shortcutVariants))
	for _, v := range shortcutVariants {
		g := b.ifTextIs(modelText, v.key)
		groups = append(groups, g)
		ask := newUUID()
		b.add("askllm", askLLMParams{
			UUID:                   ask,
			WFLLMModel:             v.model,
			WFGenerativeResultType: "Text",
			FollowUp:               false,
			WFAllowWebSearch:       v.web,
			WFLLMPrompt:            textWith("", actionOutput(promptValue, outputNameDictionaryValue)),
		})
		b.output(textWith("", actionOutput(ask, outputNameResponse)))
		b.otherwise(g)
	}
	b.output(textWith(shortcutUnknownModelText, actionOutput(modelText, outputNameText)))
	for i := len(groups) - 1; i >= 0; i-- {
		b.endIf(groups[i])
	}
	b.endIf(marker)

	return workflow{
		WFWorkflowMinimumClientVersionString: "900",
		WFWorkflowMinimumClientVersion:       900,
		WFWorkflowClientVersion:              "4018.0.4",
		WFWorkflowIcon: wfIcon{
			WFWorkflowIconStartColor:  946986751,
			WFWorkflowIconGlyphNumber: 61699,
		},
		WFWorkflowActions:                   b.actions,
		WFWorkflowHasShortcutInputVariables: true,
		WFWorkflowInputContentItemClasses:   []string{"WFStringContentItem", "WFGenericFileContentItem"},
		WFWorkflowOutputContentItemClasses:  []string{"WFStringContentItem"},
		WFWorkflowHasOutputFallback:         false,
		WFWorkflowTypes:                     []string{"QuickActions"},
		WFQuickActionSurfaces:               []string{},
		WFWorkflowImportQuestions:           []string{},
	}
}

// currentShortcutActionCount is how many WFWorkflowActions entries the
// current generator (buildWorkflow) produces, computed from the real
// generator so it can never drift from it.
var currentShortcutActionCount = sync.OnceValue(func() int {
	return len(buildWorkflow().WFWorkflowActions)
})

// newUUID returns a random RFC 4122 version 4 UUID, upper-cased to match
// the style Shortcuts itself uses (e.g. "071CFEDD-6402-4772-A3CC-9A9B4EEB1123").
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read on darwin doesn't fail in practice
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.ToUpper(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// sanitizeFilename makes name safe to use as a path component. Shortcut
// names may contain spaces, "·" and "+" (all fine on APFS/HFS+); only "/"
// (a path separator) and NUL are actually unsafe, and NUL can't appear in
// a Go string from TOML/flags in practice.
func sanitizeFilename(name string) string {
	return strings.ReplaceAll(name, "/", "-")
}

// GenerateShortcut builds shelp's shortcut, signs it, and writes
// "<name>.shortcut" (plus intermediate files) into dir, which must already
// exist. It returns the path to the signed, importable file. Because the
// plist has no WFWorkflowName, name becomes the file's basename -- and
// therefore the name Shortcuts will import it under, and the name shelp
// must later look it up by (via `shortcuts list` / `shortcuts run`).
//
// This never opens or imports the result; it only writes files and runs
// `plutil`/`shortcuts sign`, neither of which prompts the user.
func GenerateShortcut(ctx context.Context, dir, name string) (string, error) {
	base := sanitizeFilename(name)
	data, err := json.Marshal(buildWorkflow())
	if err != nil {
		return "", fmt.Errorf("marshal workflow: %w", err)
	}

	jsonPath := filepath.Join(dir, base+".json")
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write workflow json: %w", err)
	}

	unsignedPath := filepath.Join(dir, base+".unsigned.shortcut")
	res, err := RunCmd(ctx, nil, nil, "plutil", "-convert", "binary1", jsonPath, "-o", unsignedPath)
	if err != nil {
		return "", fmt.Errorf("plutil: %w", err)
	}
	if res.ExitCode != 0 {
		msg := FirstLine(res.Stderr)
		if msg == "" {
			msg = FirstLine(res.Stdout)
		}
		return "", fmt.Errorf("plutil convert failed: %s", msg)
	}

	signedPath := filepath.Join(dir, base+".shortcut")
	if err := signShortcut(ctx, unsignedPath, signedPath); err != nil {
		return "", err
	}
	return signedPath, nil
}

// signRetryDelays are the waits between `shortcuts sign` attempts. Signing
// is a round trip to Apple's server, which fails now and then for no
// reason of ours -- HTTP 500s, and once (2026-10-09) a 20-second stall
// reported as "The file doesn't exist." with the input file right there --
// and the next attempt usually works. A package var so tests can shrink it.
var signRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// signCmd runs `shortcuts sign` once; a package var so tests can fake it.
var signCmd = func(ctx context.Context, in, out string) (CmdResult, error) {
	return RunCmd(ctx, nil, nil, "shortcuts", "sign", "--mode", "anyone", "-i", in, "-o", out)
}

// signShortcut signs in to out, retrying a failed attempt after each of
// signRetryDelays.
func signShortcut(ctx context.Context, in, out string) error {
	var last string
	for attempt := 0; ; attempt++ {
		res, err := signCmd(ctx, in, out)
		switch {
		case err != nil:
			last = err.Error()
		case res.ExitCode == 0:
			return nil
		default:
			last = FirstLine(res.Stderr)
			if last == "" {
				last = FirstLine(res.Stdout)
			}
		}
		if attempt >= len(signRetryDelays) {
			return fmt.Errorf("shortcuts sign failed %d times (it goes through Apple's signing server, which fails now and then; try again in a minute): %s", attempt+1, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(signRetryDelays[attempt]):
		}
	}
}
