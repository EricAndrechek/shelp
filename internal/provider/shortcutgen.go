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
)

// wfIcon is WFWorkflowIcon.
type wfIcon struct {
	WFWorkflowIconStartColor  int `json:"WFWorkflowIconStartColor"`
	WFWorkflowIconGlyphNumber int `json:"WFWorkflowIconGlyphNumber"`
}

// wfAttachment is one entry of a WFTextTokenString's attachmentsByRange.
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

// askLLMParams is WFWorkflowActionParameters for is.workflow.actions.askllm.
type askLLMParams struct {
	UUID                   string      `json:"UUID"`
	WFLLMModel             string      `json:"WFLLMModel"`
	WFGenerativeResultType string      `json:"WFGenerativeResultType"`
	FollowUp               bool        `json:"FollowUp"`
	WFAllowWebSearch       bool        `json:"WFAllowWebSearch"`
	WFLLMPrompt            wfTextToken `json:"WFLLMPrompt"`
}

// outputParams is WFWorkflowActionParameters for is.workflow.actions.output.
type outputParams struct {
	WFOutput wfTextToken `json:"WFOutput"`
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
// for a freshly generated action UUID.
func buildWorkflow(model string, web bool) workflow {
	uuid := newUUID()
	return workflow{
		WFWorkflowMinimumClientVersionString: "900",
		WFWorkflowMinimumClientVersion:       900,
		WFWorkflowClientVersion:              "4018.0.4",
		WFWorkflowIcon: wfIcon{
			WFWorkflowIconStartColor:  946986751,
			WFWorkflowIconGlyphNumber: 61699,
		},
		WFWorkflowActions: []wfAction{
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
