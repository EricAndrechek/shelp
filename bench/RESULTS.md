# shelp bench results

_generated 2026-09-30 · 81 cases · up to 1 sample(s) per trial_

## Summary: provider x template

| Provider | Template | Context | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| antigravity:claude-sonnet-4-6 | v2 | os,shell | 57 | 26% | 97% | 93% | 59% | 2% | 8747ms/31387ms | 4327ms | 8795ms | 3981ms |
| antigravity:gemini-3.1-pro-low | v2 | os,shell | 48 | 40% | 95% | 95% | 98% | 4% | 9255ms/16557ms | 12800ms | 13413ms | 9255ms |
| antigravity:gemini-3.8-flash-low | v2 | os,shell | 77 | 39% | 100% | 100% | 100% | 26% | 6182ms/10377ms | 5785ms | 7239ms | 6317ms |
| antigravity:gpt-oss-120b-medium | v2 | os,shell | 2 | 0% | 0% | 0% | 0% | 100% | -/- | - | - | - |
| claude:haiku | v1 | os,shell | 112 | 99% | 90% | 86% | 98% | 1% | 5717ms/17607ms | 3762ms | 4163ms | - |
| claude:haiku | v2 | os,shell | 336 | 100% | 83% | 78% | 91% | 0% | 2030ms/9660ms | 2776ms | 3184ms | 1857ms |
| claude:haiku | v3 | os,shell | 112 | 96% | 88% | 84% | 96% | 4% | 6315ms/20340ms | 5305ms | 5680ms | - |
| claude:sonnet | v1 | os,shell | 112 | 100% | 98% | 97% | 91% | 0% | 2196ms/2958ms | 4012ms | 4369ms | - |
| claude:sonnet | v2 | os,shell | 224 | 100% | 97% | 96% | 100% | 0% | 2277ms/2884ms | 3260ms | 3729ms | 2260ms |
| claude:sonnet | v3 | os,shell | 112 | 100% | 98% | 97% | 100% | 0% | 2177ms/2649ms | 3219ms | 3683ms | 3121ms |
| codex:default | v1 | os,shell | 112 | 76% | 95% | 93% | 100% | 24% | 4560ms/8644ms | 4526ms | 4526ms | 4682ms |
| codex:default | v2 | os,shell | 112 | 75% | 96% | 93% | 100% | 25% | 4019ms/7931ms | 4938ms | 4938ms | - |
| codex:default | v3 | os,shell | 112 | 77% | 96% | 94% | 100% | 23% | 4297ms/7506ms | 5366ms | 5366ms | - |
| copilot:default | v1 | os,shell | 112 | 100% | 94% | 91% | 100% | 0% | 4383ms/7612ms | 3592ms | 3821ms | 6362ms |
| fm | v1 | os,shell | 112 | 100% | 31% | 26% | 100% | 0% | 499ms/922ms | 355ms | 1024ms | - |
| fm | v2 | os,shell | 112 | 100% | 36% | 29% | 100% | 0% | 586ms/1002ms | 385ms | 2392ms | - |
| fm | v2 | os,shell,tools | 112 | 100% | 32% | 28% | 100% | 0% | 724ms/1216ms | 373ms | 2401ms | 1405ms |
| fm | v3 | os,shell | 112 | 100% | 33% | 29% | 100% | 0% | 607ms/1100ms | 392ms | 2390ms | 2093ms |
| shortcuts:apple-pro | v1 | os,shell | 112 | 29% | 94% | 88% | 97% | 71% | 1319ms/1544ms | 1507ms | 1507ms | - |
| shortcuts:apple-pro | v2 | os,shell | 112 | 28% | 98% | 97% | 100% | 72% | 1295ms/1508ms | 1717ms | 1717ms | - |
| shortcuts:apple-pro | v3 | os,shell | 112 | 27% | 98% | 97% | 100% | 73% | 1242ms/1471ms | 1666ms | 1666ms | 2089ms |

## By difficulty

| Difficulty | Trials | Graded% | Mean score% | Fully-correct% |
|---|---|---|---|---|
| easy | 1133 | 83% | 85% | 83% |
| medium | 1053 | 81% | 75% | 71% |
| hard | 238 | 80% | 68% | 63% |

## By mode

| Mode | Trials | Graded% | Mean score% | Fully-correct% |
|---|---|---|---|---|
| suggest | 2103 | 81% | 77% | 74% |
| explain | 321 | 87% | 91% | 84% |

## Per-case grid

✅ mean 2 · 🟡 mean 1 · ❌ mean 0 · · ungraded

| Case | antigravity:claude-sonnet-4-6 | antigravity:gemini-3.1-pro-low | antigravity:gemini-3.8-flash-low | antigravity:gpt-oss-120b-medium | claude:haiku | claude:sonnet | codex:default | copilot:default | fm | shortcuts:apple-pro |
|---|---|---|---|---|---|---|---|---|---|---|
| add-user-to-group | · | · | ✅ 2.0 | · | ✅ 1.8 | ✅ 2.0 | · | ✅ 2.0 | 🟡 1.2 | · |
| alias-persist | · | · | · | · | 🟡 1.2 | 🟡 1.3 | ✅ 2.0 | 🟡 1.3 | ❌ 0.1 | · |
| battery-status | · | · | · | · | ✅ 1.6 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| brew-cleanup | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| cert-expiry | · | · | · | · | ✅ 2.0 | ✅ 2.0 | 🟡 1.3 | ✅ 2.0 | ❌ 0.2 | ✅ 2.0 |
| convert-heic | · | · | · | · | ✅ 1.6 | ✅ 1.8 | · | ✅ 2.0 | ❌ 0.0 | · |
| copy-file-to-clipboard | · | · | · | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| count-by-extension | · | · | · | · | 🟡 1.1 | ✅ 2.0 | ✅ 1.8 | ✅ 1.5 | ❌ 0.0 | · |
| count-unique-ips | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| cpu-model | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · | ✅ 1.9 | ✅ 2.0 | · | ✅ 2.0 | 🟡 0.5 | · |
| dedupe-keep-order | · | · | · | · | ✅ 1.6 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| delete-old-logs | 🟡 1.0 | ✅ 2.0 | ✅ 2.0 | · | 🟡 1.3 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · |
| disk-usage-folders | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| download-resume | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| empty-current-dir | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.2 | · |
| empty-dirs | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.5 | · |
| explain-awk-pipeline | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | 🟡 1.2 | ✅ 2.0 |
| explain-curl-pipe-sh | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ✅ 1.7 |
| explain-docker-run | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ✅ 2.0 |
| explain-find-delete | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 |
| explain-find-xargs0 | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 |
| explain-fork-bomb | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| explain-lsof-listen | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ✅ 2.0 |
| explain-redirect-order | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| explain-reset-hard | · | · | · | · | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 1.5 | ✅ 2.0 |
| explain-rsync-delete | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 1.7 | 🟡 1.0 | ✅ 1.5 | ✅ 1.7 |
| explain-sed-bsd | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.2 | ✅ 2.0 |
| explain-set-euo | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.2 | ✅ 2.0 |
| explain-ssh-tunnel | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ✅ 2.0 |
| explain-tar-extract | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 |
| extract-emails | · | · | · | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | 🟡 0.5 | · |
| files-containing-text | ✅ 2.0 | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ✅ 2.0 | ❌ 0.0 | · |
| files-over-100mb | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · | ✅ 1.7 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 |
| find-exclude-node-modules | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | · |
| flush-dns | · | · | ✅ 2.0 | · | 🟡 0.6 | ✅ 2.0 | ✅ 1.7 | ✅ 2.0 | ❌ 0.0 | · |
| git-biggest-history-objects | · | · | · | · | 🟡 0.7 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| git-branches-by-recent | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| git-delete-merged | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| git-find-deleted-file | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| git-last-commit-files | · | ✅ 2.0 | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.2 | · |
| git-squash-last-3 | · | · | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| git-stash-untracked | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | ✅ 2.0 |
| git-undo-last-commit | ✅ 2.0 | ✅ 2.0 | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| git-who-changed-line | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| http-headers | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.8 | · |
| jq-extract-names | · | · | · | · | ✅ 1.8 | ✅ 2.0 | · | ✅ 2.0 | 🟡 0.5 | · |
| keep-running-after-logout | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | · |
| kill-port | ✅ 2.0 | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | · |
| largest-10-here | · | ✅ 2.0 | · | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.2 | · |
| last-command-exit | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | · |
| lines-between-markers | · | · | · | · | ✅ 1.8 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| local-ip-mac | · | · | · | · | 🟡 1.4 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| loop-lines-of-file | · | · | ✅ 2.0 | · | ✅ 1.7 | ✅ 2.0 | ✅ 2.0 | 🟡 1.3 | 🟡 0.7 | · |
| make-executable | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| modified-last-24h | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.5 | ✅ 2.0 |
| mov-to-mp4 | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| open-dir-gui | · | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | 🟡 1.2 | · |
| parallel-downloads | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| port-forward-k8s | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| port-listener | · | ✅ 2.0 | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 1.5 | · |
| posix-portable-date | · | · | ✅ 2.0 | · | ✅ 1.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | · |
| prevent-sleep | · | · | · | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| public-ip | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| rename-extension-loop | · | · | · | · | ✅ 1.9 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.1 | · |
| rename-lowercase | · | · | · | · | 🟡 0.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| resize-images | · | ✅ 2.0 | · | · | 🟡 0.8 | ✅ 2.0 | ✅ 2.0 | 🟡 1.0 | ❌ 0.0 | 🟡 1.0 |
| sed-replace-recursive | · | · | ✅ 2.0 | · | 🟡 1.4 | ✅ 2.0 | · | ✅ 2.0 | 🟡 1.0 | · |
| service-logs-last-hour | · | · | ✅ 2.0 | · | 🟡 0.8 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 0.5 | · |
| set-env-persist | · | 🟡 1.0 | ✅ 2.0 | · | 🟡 1.1 | 🟡 1.3 | 🟡 1.4 | 🟡 1.3 | 🟡 0.7 | · |
| sha256-verify | ✅ 2.0 | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| show-hidden-finder | · | · | · | · | ✅ 2.0 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| ssh-with-key | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| sum-csv-column | · | · | · | · | ❌ 0.0 | ✅ 2.0 | · | ❌ 0.0 | ❌ 0.0 | · |
| tar-exclude | · | · | ✅ 2.0 | · | 🟡 1.2 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | 🟡 1.2 | ✅ 1.7 |
| tcp-port-open | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | · |
| top-memory | ✅ 2.0 | · | ✅ 2.0 | · | 🟡 1.4 | ✅ 2.0 | 🟡 1.3 | ✅ 1.5 | 🟡 0.5 | · |
| trailing-whitespace | · | · | · | · | 🟡 1.4 | ✅ 2.0 | · | ✅ 2.0 | ❌ 0.0 | · |
| unzip-to-dir | ✅ 2.0 | · | ✅ 2.0 | · | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 |
| video-to-gif | · | · | · | · | ✅ 1.6 | ✅ 2.0 | ✅ 2.0 | ✅ 2.0 | ❌ 0.0 | ✅ 2.0 |
| watch-command | · | · | · | · | ✅ 2.0 | ✅ 2.0 | ✅ 1.8 | ✅ 2.0 | ❌ 0.0 | · |
| zsh-log-builtin | · | · | ✅ 2.0 | · | ✅ 1.5 | ✅ 1.5 | ✅ 1.5 | ✅ 1.5 | ❌ 0.0 | · |

## Prompt templates (same provider set)

Providers common to every template within a given context: claude:haiku, claude:sonnet, codex:default, fm, shortcuts:apple-pro

| Template | Context | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% |
|---|---|---|---|---|---|---|
| v1 | os,shell | 560 | 81% | 79% | 75% | 97% |
| v2 | os,shell | 896 | 88% | 83% | 79% | 96% |
| v3 | os,shell | 560 | 80% | 79% | 77% | 99% |
| v2 | os,shell,tools | 112 | 100% | 32% | 28% | 100% |
