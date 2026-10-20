# Apple Shortcuts ("Use Model") backend: the deep-dive

Moved out of CLAUDE.md verbatim (2026-10-20). CLAUDE.md keeps the rules; this has the history, formats, traps and recipes. Verified on macOS 27.0, M4 Pro.

### Shortcuts "Use Model" (Apple cloud / ChatGPT)
- **One shortcut, six providers** (format 5): `shelp setup shortcuts`
  installs a single shortcut, `shelp` (`[shortcuts] name`), and all six
  `shortcuts:*` IDs (`apple`, `apple-web`, `apple-pro`, `apple-pro-web`,
  `chatgpt`, `chatgpt-web`) run it; the request says which model. The
  ChatGPT ones also need the ChatGPT extension (System Settings › Siri).
  Formats 1-4 were one shortcut per variant, `shelp · <Model>[ + Web]`;
  those names (and `<name> N` copies) are "legacy": setup, a `shelp
  providers` note, and the runtime hint list them for the user to delete
  (`legacyShortcuts`, names from `shortcuts list` only) and nothing ever
  runs them -- an old one may fail, with a notification, every run.
- Action `is.workflow.actions.askllm`. Params: `WFLLMModel` one of
  `"Apple Intelligence"`, `"Apple Intelligence Pro"`, `"ChatGPT"`;
  `WFAllowWebSearch` (UI: "Use Broad World Knowledge"); `FollowUp` false;
  `WFGenerativeResultType` "Text"; `WFLLMPrompt` a WFTextTokenString with
  one attachment (formats 1-4: `{"Type":"ExtensionInput"}`, Shortcut
  Input; 5: the "prompt" Dictionary Value). Output via
  `is.workflow.actions.output` with `WFOutput` -> ActionOutput "Response".
  Format lifted from WorkflowKit's `Gallery.bundle/.../Haiku.wflow`; enum
  values from the dyld shared cache.
- Build: JSON -> `plutil -convert binary1` -> `shortcuts sign --mode anyone`
  -> `open file.shortcut` -> the user MUST click "Add Shortcut" (no API).
  Detect installation with `shortcuts list`.
- The generated plist has no `WFWorkflowName` field: a shortcut's display
  name comes from the `.shortcut` file's basename at import time, so the
  generator writes `"<name>.shortcut"` verbatim rather than embedding a
  name anywhere in the plist.
- Run: `printf '%s' "$input" | shortcuts run NAME -o - --output-type public.plain-text`
  (default output is RTF; `public.text` crashes). ~1.2-1.7s. Missing
  shortcut: exit 1, "Couldn't find shortcut". Format 5's input is JSON,
  `{"model":"<variant key>","prompt":"<system>\n\n<prompt>"}`
  (`shortcutRequestBody`; Use Model has no separate system field). An
  unknown model answers `shelp-shortcut-error: unknown model <key>` (exit
  0, no model call), which `Generate` turns into an Unavailable.
- "Apple Intelligence Pro" verified in logs as
  `instruct_server_v2.shortcuts_use_model_pro` via `privatecloudcomputed`;
  plain "Apple Intelligence" as `instruct_server_v2.shortcuts_use_model_action`,
  also on Private Cloud Compute (2026-10-09) -- NOT the on-device model `fm`
  runs, so it isn't a duplicate of `fm`. Both also load the on-device
  `instruct_300m.safety` classifier. ChatGPT asks once per shortcut for
  consent to share the input ("Always Allow"). Verify backends with
  `/usr/bin/log show` (never bare `log` in zsh: builtin).
- **Pre-signed shortcut**: each release embeds the current format already
  signed (`internal/provider/signed/`), so setup opens it with no trip to
  Apple; it signs at runtime only when the copy is another format, its
  `WorkflowFingerprint` (the generated workflow with UUIDs normalized)
  differs, or its signature is within 30 days of expiring. **Whenever
  the generator changes, run `make sign-shortcut` and commit the result**
  -- `TestEmbeddedShortcutCurrent` fails until you do, and also when the
  copy is within 60 days of expiry (re-sign before releasing). A
  `--mode anyone` signature is an Apple-issued certificate with no
  personal identity (CN = random hex, O=Apple Inc., chaining to Apple
  Root CA - G3); the leaf is valid ~13 months (2026-10-09's: to
  2027-11-04). The file is an AEA1 archive: "AEA1", a 4-byte profile, a
  little-endian uint32 auth length, then a binary plist auth blob,
  `{SigningCertificateChain: [<DER>...]}` (plutil's JSON output can't
  hold <data>; the tool converts to XML). Signed copies import under
  whatever file name setup gives them, so `[shortcuts] name` still works.
  Verified live 2026-10-09: setup opened the embedded copy with no
  signing, Add, `✓ Installed "shelp" (v6).`
- `shortcuts sign` is a round trip to Apple's server and fails now and
  then (HTTP 500s; once a 20s stall reported as "The file doesn't exist."
  with the input file present, after a ~20s stall -- a good attempt
  takes ~2s); `signShortcut` gives each attempt 10s and makes up to 4,
  and setup prints a line per retry (`signNotice`).
- PCC has an unpublished daily cap (higher with iCloud+).

#### Shortcut format versions, update detection, and the update flow

- `ShortcutFormatVersion` (Go const in `shortcutgen.go`) is the current
  generated shortcut's plist format: **1** (shelp 0.1.x) was the
  original, marker-less, 2-action (Use Model + Stop and Output) shape
  above. **2** (0.2.0-0.2.1) added a leading version-marker `If` block
  and **3** (0.2.2) fixed its `WFInput` wrapper, but both fail EVERY run,
  real prompts included, at the If with "Please choose a value for each
  parameter in this action." (+ a notification). **4** (0.2.3-0.2.6)
  is the first one verified live (2026-10-07: marker -> `shelp-shortcut
  4`, a real prompt -> a command): "Text: Shortcut Input. If Text is
  `__shelp_shortcut_version__`, Stop and Output the text `shelp-shortcut
  4` (no model call); Otherwise, `<the v1 pair>`; End If" -- 7 actions.
  **5** (verified live 2026-10-09: marker, unknown model, one prompt each
  through apple-pro, apple and chatgpt) is one shortcut for every model,
  40 actions (`currentShortcutActionCount()`; `buildWorkflow`'s doc
  comment draws it): v4's marker block unchanged, then in its Otherwise
  Get Dictionary from Input (on the Text), Get Dictionary Value "model"
  and "prompt", Text holding the model value, and six If/Otherwise
  branches nested one inside the next ("If model-Text is `<key>`: Use
  Model (that model, web flag) on the prompt value; Stop and Output its
  Response"), the innermost Otherwise a Stop and Output of
  `shelp-shortcut-error: unknown model <model-Text>`, then seven End Ifs.
  **6** (current; verified live 2026-10-09: install, marker, a ChatGPT
  prompt, and a delete-first update to a throwaway v7) adds one Text action holding the
  "prompt" Dictionary Value and feeds Use Model that Text (41 actions):
  with the bare Dictionary Value, ChatGPT's consent dialog ("Allow
  "shelp" to share 1 dictionary with ChatGPT?") showed the whole JSON
  request, system prompt included. It still does with the Text: the
  dialog shows what the shared value was derived from (the parsed input),
  not what the model gets. An echo test settled that (2026-10-09):
  `{"model":"apple-pro","prompt":"Repeat this message back word for
  word, and nothing else."}` came back as that sentence, no JSON -- every
  branch reads the same Text, so Use Model gets only the prompt.
  The reply has no trailing newline, so when checking it by hand pipe it
  through `od -c`: an interactive prompt can draw over it and make it
  look empty.
- Format 5's action shapes, each copied from a stored one (sources cited
  in `shortcutgen.go`): `is.workflow.actions.detect.dictionary` `{UUID,
  WFInput: <WFTextTokenAttachment of a Text output>}`, output
  "Dictionary" (library: "XML To Dictionary" #8, "Wolfram Alpha 2
  Clipboard" #5); `is.workflow.actions.getvalueforkey` `{UUID,
  WFDictionaryKey, WFInput: <WFTextTokenAttachment of the Dictionary>}`,
  output "Dictionary Value", no `WFGetDictionaryValueType` (Wolfram
  #6-10); a "Dictionary Value" inside a WFTextTokenString (Wolfram #11's
  URL); a mid-string attachment keyed by its U+FFFC's UTF-16 offset
  (Wolfram #3, `{44, 1}`); If nested in Otherwise, inner End If first
  (PDFtoolkit #6-21, Set Alarms #8-15); Use Model's prompt as an
  ActionOutput (viticci EXAMPLES.md "AI Query", VARIABLES.md). macOS
  27's "Otherwise If" (mode 1 + condition fields, per viticci
  CONTROL_FLOW.md) is in no library shortcut, so v5 nests instead.
- The `is.workflow.actions.conditional` action (`If`/`Otherwise`/`End If`)
  repeats one identifier three times, sharing a `GroupingIdentifier` and
  varying `WFControlFlowMode` (`0` If, `1` Otherwise, `2` End If, which
  alone carries a `UUID`). Only the `If` sets `WFCondition` (`4` = "Is"
  on text), `WFConditionalActionString` (the literal), and `WFInput`,
  which is wrapped: `{"Type":"Variable","Variable":{"Value":<attachment>,
  "WFSerializationType":"WFTextTokenAttachment"}}` (v2 put the attachment
  there bare). Two traps, both silent at import and fatal at run time:
  the subject must have a TEXT type for "Is" -- Shortcut Input of a
  shortcut that accepts text or files doesn't (v3), so v4 compares the
  `ActionOutput` (`OutputName` "Text") of an `is.workflow.actions.gettext`
  holding Shortcut Input, the way Apple-built shortcuts compare text; and
  a missing wrapper (v2). No Gallery `.wflow` has a conditional; sources:
  the 20 If actions in Eric's own library, and
  `viticci/shortcuts-playground-plugin`'s CONTROL_FLOW.md (macOS 27, checked
  against Apple-built samples). Newer Shortcuts also has a multi-condition
  `WFConditions` (`WFContentPredicateTableTemplate`) form; the
  single-condition form still works.
- **Debugging a failed run**: `/usr/bin/log show --start "<time>" --style
  compact` filtered to `BackgroundShortcutRunner` lists each action as it
  runs ("Running action <WFTextAction ...>", "finished with error {domain:
  ConditionalAction, code: 1}", `WFActionIndex`), which is how v3's If and
  v4's untouched model branch were confirmed without running anything.
- **Reading what Shortcuts actually stored** (no side effects, the way
  the v2 bug was found): copy `~/Library/Shortcuts/Shortcuts.sqlite*` to
  the scratchpad, then `ZSHORTCUT` (`ZNAME`, `ZACTIONCOUNT`, `ZACTIONS`)
  -> `ZSHORTCUTACTIONS.ZDATA`, a binary plist of the action array
  (`plutil -convert json`). Private schema: for debugging, never for
  shelp itself.
- **Detection** (`installedShortcutVersion`, `shortcuts.go`): Shortcuts
  Events' read-only `action count` (`osascript -e 'tell application
  "Shortcuts Events" to get action count of shortcut "NAME"'`, ~150ms,
  never runs it) is a count Shortcuts caches on save -- a shortcut
  imported from a `.shortcut` file, i.e. every one shelp installs, reads
  **0** ("No actions") with all its actions stored (verified 2026-10-07).
  So: count 2 -> v1 (never sent the marker: its only real action is Use
  Model); count 0 or >= 6 -> run it with the marker as input and parse
  `shelp-shortcut N` (`shortcutMarkerVersionCmd`); anything else -> not a
  shelp format. Any failure (Shortcuts Events down, a v2/v3 that fails
  every run) is "unknown", never outdated. `Detect` reports a provider
  available only when the shortcut is listed and answers v5 or newer
  (an older one would hand the JSON to the model as the prompt);
  outdated (`outdated shortcut (format vN, current vM)`) and unknown are
  unavailable with Fix "run `shelp setup shortcuts`". The version check
  runs once per process for all six providers (`cachedShortcutVersion`).
- **Setup wording**: short plain sentences, no `shortcuts:` prefix and no
  dot trails; waits show a `style.StartSpinner` line (terminal only).
  `shelp providers` folds `-web` twins with the same status into one `add -web
  to let the model look things up online (slower), e.g. <id>` line (`webTwins`;
  the example prefers a twin of a base row in the chain; `--all` shows them).
  Available rows show a dim third column from the optional `provider.Noter`
  (`NoteOf`; where it runs and how it compares); `--json` rows carry it as
  `Note`. Labels stay short ("Claude Haiku"); the headless/no-tools fact lives
  in `shelp help providers`.
- **Update flow** (`shelp setup shortcuts`, `runShortcutsSetup`): the one
  shortcut is *missing* (generate, sign, open, user clicks "Add
  Shortcut", `waitForNewShortcut`), *outdated* (DELETE FIRST: generate
  and sign, then ask the user to delete the old one -- right-click ›
  Delete -- and poll `shortcuts list`, running nothing, until the name
  is gone (`waitForShortcutGone`); only then open the new one, so the
  import is a plain "Add Shortcut" with no conflict, and wait as for a
  missing one), *current/newer* (skip), or *unknown version* (left
  alone, with a note to delete a broken one by hand and rerun --
  replacing it would mean polling a shortcut that fails every run, one
  notification per poll). The wait confirms the marker version and asks
  at most 3 times (`maxMarkerErrors`) before giving up, so a broken new
  format can't post a notification every second. Polls run every 1s.
  Legacy shortcuts and existing `<name> N` copies are pointed out and
  never run.
- **Importing over an existing name** (verified with Eric, macOS 27,
  2026-10-07): the dialog says "Add Shortcut" (not "Replace"); clicking
  it asks "Replace" or "Keep Both", and NEITHER deletes anything. Replace:
  the new shortcut takes the name, the old one is renamed `<name> 1`.
  Keep Both: the new one is `<name> 1`. N can skip numbers (names of
  deleted shortcuts still count). Between "Add Shortcut" and the
  Replace/Keep Both answer, BOTH shortcuts carry the name (`shortcuts
  list` prints it twice) and `shortcuts run <name>` may reach the new one
  -- v4's wait took that for success and missed the leftover copy. There
  is no delete or rename API (Shortcuts Events and `shortcuts` can only
  list/run/view/sign), hence delete-first. `waitForNewShortcut` still
  copes if the name is taken anyway: it counts raw `shortcuts list`
  lines and, while the name appears more than once, runs nothing and
  never finishes; then name answering the new version means Replace (it
  asks the user to delete the new `<name> N` copy and waits), and a copy
  that appeared during the wait answering it means Keep Both (it asks the
  user to delete the old one and rename the copy). It only ever runs the
  marker on the name and on copies that appeared during the wait. A
  shortcut imported from a file gets a cached action count once
  Shortcuts modifies it (the renamed copy's count went 0 -> 7).
- **Runtime hint**: a normal `shelp suggest`/`explain` call whose resolved
  provider chain names a `shortcuts:*` provider runs a best-effort,
  once-per-shelp-binary-version check (`provider.CheckShortcutsFreshness`,
  wired into `cmd/shelp/main.go` via `defer` right after the real
  `provider.Chain` call succeeds, so it only ever runs after the answer
  is already printed and never delays or fails the real command) and
  prints one line to stderr (`shortcutsFreshnessHint`) if the shortcut is
  outdated, or not installed while legacy ones are (the first run after
  upgrading to format 5):
  ```
  shelp: your Apple Shortcut is from an older shelp; run `shelp setup shortcuts` to update it
  shelp: shelp now uses one Apple Shortcut for every shortcuts:* provider; run `shelp setup shortcuts` to install it (and delete the old ones)
  ```
  Its version check is a marker run (see Detection), so on a v2 it fails
  once per binary version, posting one notification.
  Recorded in `$XDG_CACHE_HOME/shelp/shortcut-check.json`
  (`~/.cache/shelp/...` by default, alongside `cooldown.json`) so it only
  ever runs once per binary version. This is what a `brew upgrade shelp`
  caveat points at (see `Formula/shelp.rb`) -- it's the mechanism that
  actually tells a user their Shortcuts wrapper needs updating, since
  Homebrew itself has no way to.

