package provider

// Builds, converts, and signs one Apple Shortcuts "Use Model" shortcut for
// a given (model, web-search) pair.
//
// The plist shape below was reverse-engineered from the installed "Ask AI"
// shortcut on macOS 27.0 (see CLAUDE.md, "Shortcuts Use Model") and is
// intentionally minimal: it has no WFWorkflowName field, so the shortcut's
// display name comes from the *filename* of the .shortcut file at import
// time. GenerateShortcut therefore writes "<name>.shortcut" verbatim.
//
// Pipeline: build JSON in Go -> `plutil -convert binary1` (JSON -> binary
// plist) -> `shortcuts sign --mode anyone` (binary plist -> importable
// .shortcut). Both steps are silent CLI operations; nothing pops a dialog
// until the resulting file is opened (which only the setup step, deliberately,
// ever does).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// wfIcon is WFWorkflowIcon.
type wfIcon struct {
	WFWorkflowIconStartColor  int `json:"WFWorkflowIconStartColor"`
	WFWorkflowIconGlyphNumber int `json:"WFWorkflowIconGlyphNumber"`
}

// wfAttachment is one entry of a WFTextTokenString's attachmentsByRange, or
// (wrapped in wfTextTokenAttachment) a directly-referenced single value.
// Only Type is set for the prompt's ExtensionInput attachment; the output
// action's ActionOutput attachment also sets OutputUUID/OutputName.
type wfAttachment struct {
	Type       string `json:"Type"`
	OutputUUID string `json:"OutputUUID,omitempty"`
	OutputName string `json:"OutputName,omitempty"`
}

// wfTextTokenValue is a WFTextTokenString's Value.
type wfTextTokenValue struct {
	String             string                  `json:"string"`
	AttachmentsByRange map[string]wfAttachment `json:"attachmentsByRange"`
}

// wfTextToken is a WFTextTokenString-typed parameter value (used for both
// WFLLMPrompt and WFOutput).
type wfTextToken struct {
	WFSerializationType string           `json:"WFSerializationType"`
	Value               wfTextTokenValue `json:"Value"`
}

// wfTextTokenAttachment is a parameter value that references a single
// attachment directly (Value is the attachment dict itself), rather than
// interpolating it into a string like wfTextToken does. Verified against
// macOS 27's Gallery.bundle/DocumentReview.wflow, whose "Choose from Menu"
// branches reference a file/URL the same way, e.g.
// "WFFile": {"Value": {"Type": "Ask"}, "WFSerializationType": "WFTextTokenAttachment"}.
type wfTextTokenAttachment struct {
	WFSerializationType string       `json:"WFSerializationType"`
	Value               wfAttachment `json:"Value"`
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

// askLLMParams is WFWorkflowActionParameters for is.workflow.actions.askllm.
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

// outputParams is WFWorkflowActionParameters for is.workflow.actions.output.
type outputParams struct {
	WFOutput wfTextToken `json:"WFOutput"`
}

// conditionalParams is WFWorkflowActionParameters for
// is.workflow.actions.conditional (an "If"/"Otherwise"/"End If" triplet).
// All three actions in one group share GroupingIdentifier and repeat the
// same action identifier; WFControlFlowMode distinguishes which part of the
// group a given entry is (see the WFControlFlowMode* constants below). Only
// the "If" entry sets WFCondition/WFConditionalActionString/WFInput; only
// the "End If" entry sets UUID (present but never referenced elsewhere in
// this generator -- matches the pattern of every WFControlFlowMode-based
// action observed, e.g. DocumentReview.wflow's repeat/choosefrommenu ends).
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
// github.com/electrikmilk/cherri's `conditions` map agrees.
const conditionIsEquals = 4

// ShortcutFormatVersion is the current generated shortcut's plist format.
// It increments whenever buildWorkflow's action shape changes in a way
// that changes currentShortcutActionCount() or otherwise requires an
// installed shortcut to be regenerated (see shortcuts.go's outdated-version
// detection). 1 was the original marker-less, 2-action (Use Model + Stop
// and Output) shape. 2 added the version-marker If block below, and 3
// fixed its WFInput wrapper, but both compared Shortcut Input itself, and
// a shortcut that takes text or files has no text type for "is" to
// compare: Shortcuts drops the comparison string, and every run, real
// prompts included, fails at the If with "Please choose a value for each
// parameter in this action." (live on macOS 27, 2026-10-07). 4 compares
// the output of a Text action holding Shortcut Input instead, the way
// Apple-built shortcuts do text comparisons.
const ShortcutFormatVersion = 4

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

// literalOutputParams builds an is.workflow.actions.output action whose
// WFOutput is a plain literal string (no attachment/interpolation) --
// contrast with buildWorkflow's model-response output, whose WFOutput
// attaches the askllm action's ActionOutput.
func literalOutputParams(text string) outputParams {
	return outputParams{
		WFOutput: wfTextToken{
			WFSerializationType: "WFTextTokenString",
			Value: wfTextTokenValue{
				String:             text,
				AttachmentsByRange: map[string]wfAttachment{},
			},
		},
	}
}

// wfAction is one entry of WFWorkflowActions.
type wfAction struct {
	WFWorkflowActionIdentifier string `json:"WFWorkflowActionIdentifier"`
	WFWorkflowActionParameters any    `json:"WFWorkflowActionParameters"`
}

// workflow is the unsigned .shortcut plist, as JSON, for one "Use Model"
// shortcut. Field order here doesn't matter: plutil/Shortcuts treat it as
// an unordered dictionary.
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

// buildWorkflow returns the unsigned plist (as Go structs, ready to
// marshal to JSON) for the "Use Model" action configured with model and
// web, wired to take Shortcut Input as the prompt and return the model's
// response as output. Matches the reference in CLAUDE.md exactly except
// for freshly generated UUIDs.
//
// Since ShortcutFormatVersion 2, the actual Use Model call is wrapped in a
// leading version-check block: "Text: Shortcut Input. If Text is <the
// marker string>, Stop and Output 'shelp-shortcut <N>'; Otherwise, <the
// v1 Use Model + Stop and Output pair>; End If." (The Text action is
// format 4's; see ShortcutFormatVersion.) This lets shelp ask an already-installed
// shortcut what format it is (see shortcuts.go) without ever spending a
// real model call to find out, and without risking one on a shortcut that
// might still be the marker-less v1 format (that branch never runs unless
// the input equals the exact marker string).
func buildWorkflow(model string, web bool) workflow {
	uuid := newUUID()
	textUUID := newUUID()
	groupID := newUUID()
	endUUID := newUUID()

	inputText := &wfConditionInput{
		Type: "Variable",
		Variable: wfTextTokenAttachment{
			WFSerializationType: "WFTextTokenAttachment",
			Value:               wfAttachment{Type: "ActionOutput", OutputUUID: textUUID, OutputName: "Text"},
		},
	}

	return workflow{
		WFWorkflowMinimumClientVersionString: "900",
		WFWorkflowMinimumClientVersion:       900,
		WFWorkflowClientVersion:              "4018.0.4",
		WFWorkflowIcon: wfIcon{
			WFWorkflowIconStartColor:  946986751,
			WFWorkflowIconGlyphNumber: 61699,
		},
		WFWorkflowActions: []wfAction{
			{ // Text: Shortcut Input
				WFWorkflowActionIdentifier: "is.workflow.actions.gettext",
				WFWorkflowActionParameters: textParams{
					UUID: textUUID,
					WFTextActionText: wfTextToken{
						WFSerializationType: "WFTextTokenString",
						Value: wfTextTokenValue{
							String: extensionInputPlaceholder,
							AttachmentsByRange: map[string]wfAttachment{
								"{0, 1}": {Type: "ExtensionInput"},
							},
						},
					},
				},
			},
			{ // If Text is <shortcutVersionMarker>
				WFWorkflowActionIdentifier: "is.workflow.actions.conditional",
				WFWorkflowActionParameters: conditionalParams{
					GroupingIdentifier:        groupID,
					WFControlFlowMode:         wfControlFlowStart,
					WFCondition:               conditionIsEquals,
					WFConditionalActionString: shortcutVersionMarker,
					WFInput:                   inputText,
				},
			},
			{ // Stop and Output "shelp-shortcut <N>" (no model call)
				WFWorkflowActionIdentifier: "is.workflow.actions.output",
				WFWorkflowActionParameters: literalOutputParams(shortcutVersionOutputText(ShortcutFormatVersion)),
			},
			{ // Otherwise
				WFWorkflowActionIdentifier: "is.workflow.actions.conditional",
				WFWorkflowActionParameters: conditionalParams{
					GroupingIdentifier: groupID,
					WFControlFlowMode:  wfControlFlowMiddle,
				},
			},
			{
				WFWorkflowActionIdentifier: "is.workflow.actions.askllm",
				WFWorkflowActionParameters: askLLMParams{
					UUID:                   uuid,
					WFLLMModel:             model,
					WFGenerativeResultType: "Text",
					FollowUp:               false,
					WFAllowWebSearch:       web,
					WFLLMPrompt: wfTextToken{
						WFSerializationType: "WFTextTokenString",
						Value: wfTextTokenValue{
							String: extensionInputPlaceholder,
							AttachmentsByRange: map[string]wfAttachment{
								"{0, 1}": {Type: "ExtensionInput"},
							},
						},
					},
				},
			},
			{
				WFWorkflowActionIdentifier: "is.workflow.actions.output",
				WFWorkflowActionParameters: outputParams{
					WFOutput: wfTextToken{
						WFSerializationType: "WFTextTokenString",
						Value: wfTextTokenValue{
							String: extensionInputPlaceholder,
							AttachmentsByRange: map[string]wfAttachment{
								"{0, 1}": {OutputUUID: uuid, Type: "ActionOutput", OutputName: "Response"},
							},
						},
					},
				},
			},
			{ // End If
				WFWorkflowActionIdentifier: "is.workflow.actions.conditional",
				WFWorkflowActionParameters: conditionalParams{
					GroupingIdentifier: groupID,
					WFControlFlowMode:  wfControlFlowEnd,
					UUID:               endUUID,
				},
			},
		},
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
// current generator (buildWorkflow) produces, regardless of (model, web) --
// used by shortcuts.go's action-count detection so it can never drift from
// the real generator. It's computed once (all variants produce the same
// count) rather than hardcoded.
var currentShortcutActionCount = sync.OnceValue(func() int {
	return len(buildWorkflow("Apple Intelligence", false).WFWorkflowActions)
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

// GenerateShortcut builds the "Use Model" shortcut for (model, web),
// signs it, and writes "<name>.shortcut" (plus intermediate files) into
// dir, which must already exist. It returns the path to the signed,
// importable file. Because the plist has no WFWorkflowName, name becomes
// the file's basename -- and therefore the name Shortcuts will import it
// under, and the name shelp must later look it up by by (via `shortcuts
// list` / `shortcuts run`).
//
// This never opens or imports the result; it only writes files and runs
// `plutil`/`shortcuts sign`, neither of which prompts the user.
func GenerateShortcut(ctx context.Context, dir, name, model string, web bool) (string, error) {
	base := sanitizeFilename(name)
	wf := buildWorkflow(model, web)
	data, err := json.Marshal(wf)
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
	res, err = RunCmd(ctx, nil, nil, "shortcuts", "sign", "--mode", "anyone", "-i", unsignedPath, "-o", signedPath)
	if err != nil {
		return "", fmt.Errorf("shortcuts sign: %w", err)
	}
	if res.ExitCode != 0 {
		msg := FirstLine(res.Stderr)
		if msg == "" {
			msg = FirstLine(res.Stdout)
		}
		return "", fmt.Errorf("shortcuts sign failed: %s", msg)
	}
	return signedPath, nil
}
