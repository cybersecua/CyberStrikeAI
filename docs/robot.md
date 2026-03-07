# CyberStrikeAI Bot / Chatbot Guide

[Chinese](robot.md) | [English](robot_en.md)

This document explains how to chat with CyberStrikeAI via **Lark (Feishu)** using persistent long-lived connections, so you can use it from your phone without opening a browser. Follow these steps to avoid common pitfalls.

---

## 1. Where to Configure in CyberStrikeAI

1. Log in to the CyberStrikeAI Web UI.
2. Navigate to **System Settings** in the left sidebar.
3. Click **Bot Settings** in the left settings panel (located between "Basic Settings" and "Security Settings").
4. Enable and fill in the relevant platform fields (Lark: App ID / App Secret).
5. Click **Apply Configuration** to save.
6. **Restart the CyberStrikeAI application** (saving without restarting will not establish the bot connection).

Configuration is written to the `robots` section of `config.yaml` and can also be edited directly in the config file. **After changing Lark configuration, a restart is required for the long-lived connection to take effect.**

---

## 2. Supported Platforms (Long-Lived Connection)

| Platform | Description |
|----------|-------------|
| Lark (Feishu) | Uses long-lived connection; the program actively connects to Lark to receive messages |

Section 3 below explains per-platform: what to do on the open platform, which fields to copy, and where to fill them in CyberStrikeAI.

---

## 3. Per-Platform Configuration and Setup Steps

### 3.1 Lark (Feishu)

| Field | Description |
|-------|-------------|
| Enable Lark Bot | Check to start Lark long-lived connection |
| App ID | App ID from the Lark Open Platform app credentials |
| App Secret | App Secret from the Lark Open Platform app credentials |
| Verify Token | Used for event subscription verification (optional) |

**Lark quick setup**: Log in to [Lark Open Platform](https://open.feishu.cn) → Create an internal enterprise app → Get **App ID** and **App Secret** from "Credentials & Basic Info" → Enable **Bot** under "App Capabilities" and grant the required permissions → Publish the app → Enter App ID and App Secret in CyberStrikeAI Bot Settings → Save and **restart the application**.

---

## 4. Bot Commands

Send the following **text commands** to the bot in Lark (text only):

| Command | Description |
|---------|-------------|
| **help** | Display command help and descriptions |
| **list** or **conversations** | List all conversation titles and IDs |
| **switch \<conversation-id\>** or **continue \<conversation-id\>** | Switch to the specified conversation; subsequent messages continue in that conversation |
| **new** | Start a new conversation; subsequent messages go into the new conversation |
| **clear** | Clear the current conversation context (equivalent to "new") |
| **current** | Show the current conversation ID and title |
| **stop** | Interrupt the currently running task |
| **roles** or **role list** | List all available roles (Penetration Testing, CTF, Web Application Scanning, etc.) |
| **role \<role-name\>** or **switch role \<role-name\>** | Switch to the specified role |
| **delete \<conversation-id\>** | Delete the specified conversation |
| **version** | Display the current CyberStrikeAI version number |

Any input **other than the above commands** is sent as a user message to the AI, following the same logic as the Web UI (penetration testing, security analysis, etc.).

---

## 5. How to Use (Do You Need to @ the Bot?)

- **Direct message (recommended)**: In Lark, **search for and open the bot**, enter the private chat with the bot, and type "help" or any text directly — **no @ required**.
- **Group chat**: If the bot is added to a group, only messages sent **@bot** in the group will be received and replied to; messages without @ will not trigger the bot.

Summary: In a **direct/private chat**, just send your message directly; in a **group chat**, you need to **@bot** before your message.

---

## 6. Recommended Workflow (Avoid Missing Steps)

1. **On the open platform**: Complete Lark app creation, copy credentials, enable the bot, set permissions, and publish — as described in Section 3.
2. **In CyberStrikeAI**: System Settings → Bot Settings → check the relevant platform, paste App ID and App Secret → click **Apply Configuration**.
3. **Restart the CyberStrikeAI process** (otherwise the long-lived connection will not be established).
4. **On your phone (Lark)**: Find the bot (direct chat: just send a message; group chat: @bot first), then send "help" or any content to test.

If messages get no response, check **Section 9 Troubleshooting** and **Section 10 Common Pitfalls** first.

---

## 7. Configuration File Example

Relevant `config.yaml` snippet for bot configuration:

```yaml
robots:
  lark:
    enabled: true
    app_id: "your_lark_app_id"
    app_secret: "your_lark_app_secret"
    verify_token: ""
```

After modifying, **restart the application** — the long-lived connection is established when the application starts.

---

## 8. How to Verify Without a Lark Client

If Lark is not installed, use the **test endpoint** to verify bot logic:

1. Log in to the CyberStrikeAI Web UI first (to obtain a valid session).
2. Call the test endpoint with curl (requires the login Cookie):

```bash
# Replace YOUR_COOKIE with the Cookie obtained after login
# (browser F12 → Network → any request → Request Headers → Cookie)
curl -X POST "http://localhost:8080/api/robot/test" \
  -H "Content-Type: application/json" \
  -H "Cookie: YOUR_COOKIE" \
  -d '{"platform":"lark","user_id":"test_user","text":"help"}'
```

If the JSON response contains `"reply":"[CyberStrikeAI Bot Commands]..."`, the command handling is working. You can also try `"text":"list"`, `"text":"current"`, etc.

Endpoint: `POST /api/robot/test` (requires login). Request body: `{"platform":"optional","user_id":"optional","text":"required"}`. Response: `{"reply":"reply content"}`.

---

## 9. Common Pitfalls

- **Saved but not restarted**: After changing bot configuration in CyberStrikeAI, you **must restart the application** — otherwise the long-lived connection will not be established.
- **App not published**: After modifying bot settings or permissions on the open platform, you must **publish a new version** under "Version Management & Publishing" — otherwise changes will not take effect.

---

## 10. Notes

- Lark **processes text messages only**; other types (images, voice, etc.) will either display a "not supported" notice or be ignored.
- Conversations are shared with the Web UI: conversations created via the bot appear in the Web UI's "Conversations" list, and vice versa.
- The bot's execution logic is identical to **`/api/agent-loop/stream`** (including progress callbacks and step details written to the database); the only difference is that SSE is not pushed to the client — instead the complete reply is sent back to Lark in one message at the end.
