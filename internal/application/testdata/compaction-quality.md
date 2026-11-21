# Thread compaction quality

The three cases in `compaction-quality-cases.json` each have five user messages before the first manual compaction and three before the second. The last question follows the second compaction. The release case reads the supplied older README through the normal workspace tool. Other historical assistant replies are fixed acknowledgements. Only the scorer reads `expected`.

The default offline tests send the cases through Thread, two summary commits and SQLite reopen. They check that the second summarizer sees the complete first transcript when it fits, including the original user decisions in order. If earlier messages are missing or only available as excerpts, the previous model handoff supplies the missing context. The final request includes the verified user turns, attributes the old README to its tool call and ends with the current question. When the user resubmits an earlier statement after another message, the workbench also uses the entire verified archive in place of the historical model summary if it fits the same byte and token limits. A deliberately stale model summary exercises that choice.

```powershell
go test ./internal/application -run '^(TestThreadCompactionQualitySources|TestCompactionQualityScorer)$'
```

With a configured DeepSeek account, this single command generates two real summaries and grades the real public Thread answer for each of the three cases. It normally makes nine paid requests. Qualification and retries share a hard limit of sixteen HTTP calls. The saved provider settings are opened read-only and tool execution uses the isolated case workspace and normal Run permissions.

```powershell
go test ./internal/application -run '^TestLiveThreadCompactionQuality$' -count=1 -v -args -live-compaction-quality
```

The release case first describes the current list, then announces a change, then repeats the original description. To compare it with an explicit change back, the following command runs just that control using the same question, old README, expected answer, and tool-effect checks. Only the three user statements choosing the grouping are reworded. Each starts with “本轮决定”.

```powershell
go test ./internal/application -run '^TestLiveThreadCompactionQuality$' -count=1 -v -args -live-compaction-quality -compaction-explicit-decisions
```

Set `TRAVERSE_COMPACTION_QUALITY_DIR` before the live command to keep model request bodies, summary records, SQLite snapshots and graded replies in a new `live-*` directory. Each run begins with fresh synthetic history and uses the configured route's complete context window. Each `run.json` records the effective limits. The runner compares the tool records before and after the final question. The refund forbids all tools. The handoff cases can recall existing records, including rereading the supplied old draft. An executed test or write fails the case even if the public reply says `not_run`.

To collect product requests, set the output directory to a fresh absolute path.

```powershell
$env:TRAVERSE_COMPACTION_QUALITY_DIR = Join-Path (Get-Location) '.tmp/compaction-quality-exchange'
go test ./internal/application -run '^TestThreadCompactionQualityExchange$' -count=1 -v
```

Each case exports `summary-1-request.json`, `summary-2-request.json`, then `answer-request.json`. The files contain the request captured at the provider, including the system messages, tool schemas and model options. A missing response leaves that case waiting. Submit the saved request with your model runner, write the receipt beside it as `<stage>-response.json`, and rerun the same command. If the model calls a tool on the final turn, return its complete native response and use the numbered follow-up request. The exchange itself dispatches no external requests.

A receipt has a `request_sha256` containing the lowercase SHA-256 of the original request file bytes and a `response` containing the model's complete `llm.ChatResponse` with its original text, finish reason and usage. The last response uses the product's normal root-action format. SQLite snapshots 0, 1, 2 and 3 retain the first batch, the first committed summary plus the second batch, the second committed summary, and the final public answer respectively. `result.json` grades that public message. The default route is `deepseek/deepseek-v4-flash`. Set `TRAVERSE_COMPACTION_QUALITY_ROUTE` and `TRAVERSE_COMPACTION_QUALITY_WINDOW` before the first capture to select another route or input-plus-output window. An automatic compaction in the selected window fails the two-compaction experiment.

To compare a change in final request construction with the same committed summaries, keep the original workspace in place. For every case, copy `run.json`, `snapshot-0.sqlite`, `snapshot-1.sqlite`, `snapshot-2.sqlite`, and the two `*-committed.json` files into a fresh output directory with the same case subdirectories. Run the exchange there. It uses the manifest's complete context window for the matching case and route. An explicit token limit must match the saved total. Older manifests that recorded only the total use the offline output limits. The frozen SQLite files preserve the original messages, tool results, summaries, and workspace reference. The new code constructs a fresh final request.

To grade observed replies from another harness, write a JSON object mapping case IDs to the exact public answer strings, then run the same scorer.

```powershell
$env:TRAVERSE_COMPACTION_QUALITY_ANSWERS = (Resolve-Path observed-answers.json).Path
go test ./internal/application -run '^TestObservedCompactionQualityAnswers$' -count=1 -v
```

The refund answer must be the requested single line. The handoff cases require the full field values and the correct state. JSON key order and whitespace between fields are immaterial. Extra fields, duplicate keys and a second conflicting answer fail.
