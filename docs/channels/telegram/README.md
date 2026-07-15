> Back to [README](../../../README.md)

# Telegram

The Telegram channel uses long polling via the Telegram Bot API for bot-based communication. It supports text messages, media attachments (photos, voice, audio, documents), voice transcription ([setup](../../guides/providers.md#voice-transcription)), and built-in command handling.

## Configuration

```json
{
  "channel_list": {
    "telegram": {
      "enabled": true,
      "type": "telegram",
      "token": "123456789:ABCdefGHIjklMNOpqrsTUVwxyz",
      "allow_from": ["123456789"],
      "proxy": "",
      "rich_messages": false,
      "use_markdown_v2": false,
      "media_group_delay_ms": 500,
      "dm_policy": "allowlist",
      "group_policy": "allowlist",
      "media_max_mb": 20,
      "max_album_items": 10,
      "ack_reactions": false,
      "groups": {
        "-100123456789": {
          "require_mention": true,
          "allow_from": ["123456789"],
          "topics": {
            "42": { "require_mention": false }
          }
        }
      }
    }
  }
}
```

| Field            | Type   | Required | Description                                                        |
| ---------------- | ------ | -------- | ------------------------------------------------------------------ |
| enabled          | bool   | Yes      | Whether to enable the Telegram channel                             |
| token            | string | Yes      | Telegram Bot API Token                                             |
| allow_from       | array  | No       | Allowlist of user IDs; empty means all users are allowed           |
| proxy            | string | No       | Proxy URL for connecting to the Telegram API (e.g. http://127.0.0.1:7890) |
| rich_messages    | bool   | No       | Use Bot API 10.1 rich messages, with automatic fallback to standard HTML |
| use_markdown_v2 | bool   | No       | Enable Telegram MarkdownV2 formatting                              |
| media_group_delay_ms | int | No       | Idle delay before processing Telegram media groups/albums. Defaults to 500 ms |
| dm_policy        | string | No       | `open`, `allowlist` (uses `allow_from`), or `disabled` |
| group_policy     | string | No       | `open`, `allowlist` (requires a `groups` entry), or `disabled` |
| groups           | object | No       | Per-group and per-topic `enabled`, `require_mention`, and `allow_from` overrides |
| media_max_mb     | int    | No       | Reject inbound and outbound files larger than this; disabled when unset |
| max_album_items  | int    | No       | Cap buffered inbound albums and outbound attachment batches; disabled when unset |
| ack_reactions    | bool   | No       | Add a lightweight 👀 acknowledgement to accepted inbound messages |

## Setup

1. Search for `@BotFather` in Telegram
2. Send the `/newbot` command and follow the prompts to create a new bot
3. Obtain the HTTP API Token
4. Fill in the Token in the configuration file
5. (Optional) Configure `allow_from` to restrict which user IDs can interact (you can get IDs via `@userinfobot`)

## Built-in Commands

Telegram auto-registers PicoClaw's top-level bot commands at startup, including `/start`, `/help`, `/show`, `/list`, and `/use`.

Skill-related commands:

- `/list skills` lists the installed skills visible to the current agent.
- `/list mcp` lists configured MCP servers and whether they are deferred/connected.
- `/show mcp <server>` lists the active tools for a connected MCP server.
- `/use <skill> <message>` forces a skill for a single request.
- `/use <skill>` arms the skill for your next message in the same chat.
- `/use clear` clears a pending skill override.

Examples:

```text
/list skills
/list mcp
/show mcp github
/use git explain how to squash the last 3 commits
/use git
explain how to squash the last 3 commits
```

## Lightweight interactive features

Outbound messages may include `buttons` or a native `poll`. Inline button callbacks,
non-anonymous poll answers, and message-reaction changes are delivered through the
normal inbound message path with `context.raw.event_type` set to `callback`,
`poll_answer`, or `reaction`. Poll routing keeps at most 128 poll targets in memory.

The existing `reaction` tool now works on Telegram and adds 👍 to the selected message.
Telegram only sends reaction-change updates when the bot has the required group admin
permissions.

## Advanced Formatting

The channel targets Telegram Bot API 10.2. Rich messages introduced in Bot API 10.1
can be enabled with `rich_messages: true`.
PicoClaw falls back to standard HTML when the Bot API rejects a rich message.
Leave this disabled when users rely on older
Telegram clients. `rich_messages` takes precedence over `use_markdown_v2`.

Response streaming uses one persistent Telegram message and edits it in place.
This works in private chats, groups, and forum topics and avoids the private-chat-only,
30-second lifetime of Telegram's `sendMessageDraft` previews.

You can set `use_markdown_v2: true` to enable enhanced formatting options. This allows the bot to utilize the full range of Telegram MarkdownV2 features, including nested styles, spoilers, and custom fixed-width blocks.

```json
{
  "channel_list": {
    "telegram": {
      "enabled": true,
      "type": "telegram",
      "token": "YOUR_BOT_TOKEN",
      "allow_from": ["YOUR_USER_ID"],
      "use_markdown_v2": true
    }
  }
}
```
