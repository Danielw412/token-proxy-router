# How accounts are chosen

Token Proxy Router sends each request to one of your accounts by how much quota it has
left, and keeps each conversation on one account so its prompt cache stays warm. Codex
and Claude are separate **pools**: each has its own current account, ranking, settings,
sessions, pause and history. Every other provider is left entirely to the upstream
server's own selection.

It works with no configuration. Everything below is optional.

## Two decisions

1. **The current account.** One account receives every *new* conversation. It changes
   only for a reason (below), so traffic stays on one account instead of draining them
   all at once.
2. **Each conversation's account.** A conversation stays on the account it started on
   while its prompt cache is warm, even if the current account changes meanwhile.

Prompt caching is per account, so moving a busy conversation costs a full uncached
turn. That is why two accounts can both be busy: a long conversation keeps its account
after the current account moves. It moves when it goes idle past `cache_warm_minutes`,
or when its account runs low, runs out or is set to Off.

## Conversations, subagents and forks

A conversation is recognised from what its client sends:

| Client | Identified by |
| --- | --- |
| Codex | `session_id` and `thread_id` headers, and its turn metadata |
| Claude Code | `X-Claude-Code-Session-Id`, or `metadata.user_id` in the request body when the header is absent |
| Others | Generic session headers, `prompt_cache_key` and conversation IDs in the body, or the WebSocket session |
| Anything else | An identity derived from the conversation's content |

Subagents and forks carry their parent's identity, so a main agent and all its subagents
count as one conversation and always share one account.

## When the current account changes

**Right away**, whatever else is going on, when the account:

- is below `switch_threshold_percent` on its 5-hour or weekly window (only if another
  account actually has more);
- hit a usage limit;
- was removed or disabled, or has been unavailable for several minutes;
- was set to Off.

**Switching back**, with `switch_back` on, when a better-ranked account clearly wins,
has at least `switch_threshold_percent + switch_back_margin_percent` left, and
`min_dwell_minutes` have passed since the last switch.

**By you**, with *Use now* or *Pin*. An account you chose is never moved away from
automatically except for one of the reasons in the first list.

## Ranking

Healthy normal accounts always come first. Accounts below the threshold, Reserve
accounts and exhausted accounts follow, and Off accounts are never chosen automatically.
Within that, `rank_by` decides, most important first. Each criterion only separates
accounts that tie on the ones above it.

| Criterion | Prefers | Counts as a tie when |
| --- | --- | --- |
| `account_order` | Higher in `account_order` | Same position (unlisted accounts share the last one) |
| `weekly_reset` | A weekly window that resets sooner, so its quota is used before it is lost | Resets less than 24 h apart |
| `five_hour_reset` | A running 5-hour window that resets sooner. Windows that have not started rank last, so they stay unstarted | Same reset time, or neither window running |
| `most_remaining` | More quota left, on the tighter of the two windows | Within 5 points |

Switching back needs a *clear* win on the first criterion where two accounts differ: any
difference for account order, 24 h sooner for the weekly reset, 30 min sooner for the
5-hour reset, and more than `switch_back_margin_percent` for most remaining, which only
counts when it ranks first. This is what stops the router flapping.

## Manual control

- **Use now** makes an account current and stays automatic from there.
- **Pin** sends everything to one account, including conversations already running on
  another. Choose the account under *Route all traffic through* at the top of the Routing
  page, or with *Pin* on its row. An open Codex WebSocket finishes on its account first.
  If the pinned account becomes unavailable, `manual_fallback` decides: `auto` routes
  automatically until it returns, `strict` rejects requests.
- **Automatic** returns to automatic routing.
- **Roles.** *Normal*; *Reserve* (`reserve_accounts`) is used automatically only when no
  normal account has quota; *Off* (`disabled_accounts`) is never chosen automatically,
  though you can still pin it.

## Pause and slow down

**Pause** holds new requests at the router, so nothing reaches the provider and no quota
is used. Replies already streaming finish. Sessions keep their accounts, and paused time
does not count as idle. A pause lasts until you resume or for a set time, up to 24 hours.
**Auto-resume**, every 1 to 24 minutes, lets each waiting conversation take one step, so
a long pause does not end turns and their caches stay warm.

A request that waits too long is ended with a retryable error: 25 minutes over HTTP, 290
seconds over WebSocket, or when more than 200 are waiting. The client retries, and the
retry waits again. A client that disconnects stops waiting at once.

**Slow down** makes every request wait a set number of seconds (0 to 240) first. It changes
when quota is used, not how much.

A pause or slow down applies to one pool. 5-hour window starts are never held.

## 5-hour windows

A 5-hour window starts at an account's first request. Starting it on purpose, with one
tiny request, decides when it resets.

- **Start** one account or all of them, now or after a delay, optionally staggered.
- **Schedule** a start at a time, optionally every day.
- **Auto-start** (`auto_start_windows`, off by default) starts each account's next window
  a minute after the last one resets, so a window is always running.

Each start reads quota first and sends nothing if the window is already running. It uses
`window_start_model` and leaves no conversation behind: no stored response, and a fresh
random session and cache ID each time. Only times, statuses, token counts and percentages
are kept, never the text. A start that falls due while the server is down is reported as
missed rather than sent late.

Auto-start skips an account that is Off, out of weekly quota, blocked by a usage limit or
unavailable, and one with a start you scheduled before its window would end: your times
decide. A failed automatic start is retried after 15 minutes, then 30, 60 and at most 120.

## Where the numbers come from

- **Live.** Every response carries rate-limit data, read as it passes.
- **Polling.** Idle accounts are refreshed from the provider's read-only usage endpoint
  every `quota_poll_minutes`, at most 3 per pool every 15 seconds. API-key credentials have
  no usage endpoint and rely on response headers.
- **Exhaustion.** A usage-limit rejection marks the account exhausted until its reset. Fresh
  numbers showing quota again lift that early.
- **Staleness.** When a window's reset time passes it counts as full until new numbers
  arrive. Data older than `stale_after_minutes` is flagged in the panel.

If the router fails in any way (a bug, bad data, a missing file), the upstream server's own
selection takes over for that request. The router only rejects a request for something you
chose: a strict pin, every account set to Off, or a pause past its limit. It only routes
among credentials the server already offers, and never hides which account is used.

## How Claude and Codex differ

| | Codex | Claude |
| --- | --- | --- |
| Live numbers | `x-codex-primary-*`, `x-codex-secondary-*` headers, and `codex.rate_limits` WebSocket events | `anthropic-ratelimit-unified-5h-*` and `-7d-*` headers |
| Usage endpoint | `chatgpt.com/backend-api/wham/usage` | `api.anthropic.com/api/oauth/usage`, the one the panel uses |
| Plan | From the auth file and responses | From the OAuth profile endpoint, at most once a day |
| 5-hour window not started | A reset time that keeps sliding | A null reset time |
| Usage limit | `usage_limit_reached` in the failure body | A `rejected` window status. A rejection of overage alone does not take the account out of rotation |
| Default start model | `gpt-6-luna`, effort `low` | `claude-haiku-5-5`, no effort |
| Open connections | An open Responses WebSocket stays on its account until it closes; new sessions and reconnects move at once | Plain HTTP, so every request can move |

Claude's per-model weekly windows (such as Opus or Sonnet) are not routed on: the router uses
the shared 5-hour and 7-day windows. A limit that applies to one model takes effect through
`model_level_limits` or the server's own cooldowns.

## Settings

Read and change them per pool with `GET` and `PATCH /router/settings?provider=`. They live in
the data folder (`data/router/<provider>.settings.json`), not `config.yaml`, and apply at once.
Out-of-range values are clamped and reported as warnings, never refused, so a bad value cannot
take routing offline.

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `enabled` | `true` | | Off leaves this pool's requests to upstream selection. |
| `switch_threshold_percent` | `5` | 0–90 | Leave an account when either window falls below this. |
| `rank_by` | `[account_order, weekly_reset, five_hour_reset, most_remaining]` | | Ranking criteria, most important first. Unknown entries are dropped, missing ones appended. |
| `strategy` | `balanced` | | Legacy preset, used only when `rank_by` is empty: `balanced`, `priority`, `soonest-reset`, `most-remaining`. |
| `switch_back` | `true` | | Return to a better-ranked account when it clearly wins. |
| `switch_back_margin_percent` | `20` | 0–90 | Headroom above the threshold needed to switch back. Also the margin a `most_remaining` win needs. |
| `min_dwell_minutes` | `20` | 0–1440 | Minimum time between non-emergency switches. |
| `cache_warm_minutes` | `30` | 0–1440 | How long an idle conversation keeps its account (an estimate of the prompt-cache lifetime). |
| `account_order` | `[]` | | Account IDs, most preferred first. Unlisted accounts share the lowest position. |
| `reserve_accounts` | `[]` | | Used automatically only when no normal account has quota. |
| `disabled_accounts` | `[]` | | Off: never chosen automatically. You can still pin them. |
| `manual_fallback` | `auto` | `auto`, `strict` | When the pinned account is unavailable: route automatically, or reject. |
| `quota_poll_minutes` | `10` | 0, or 2–1440 | How often idle accounts are checked. `0` uses live traffic and explicit refreshes only. |
| `model_level_limits` | `false` | | Scope usage-limit failures to the model that hit them. |
| `stale_after_minutes` | `30` | 1–1440 | Flag older quota data as stale. |
| `history_limit` | `200` | 10–5000 | Routing history entries kept. |
| `window_start_model` | per pool | | Model used to start 5-hour windows. |
| `window_start_effort` | per pool | | Reasoning effort for window starts (`none` to `xhigh`). |
| `auto_start_windows` | `false` | | Start each account's next window a minute after the last one resets. |

## What the upstream server should be set to

`routing.cooldown.transient-error-cooldown-seconds: -1` stops an account being cooled down for
60 seconds after an unclassified upstream error such as a WebSocket closing with 1012. Without
it, a reconnecting WebSocket can be pinned to whichever account the router detoured it to. Real
quota errors (429) still cool down as before.

## Plan sizes in pool totals

When a pool mixes plans, totals and forecasts size each account by its plan, such as Claude Max 5x
against Pro. These sizes are estimates and providers can change them. They never affect which account
a request goes to, and a pool where every account has the same plan is unaffected. There is no setting
to change them yet.
