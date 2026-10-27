# APIChat

A desktop chat app for Claude that runs on your own Windows PC. It talks to the Claude API with the owner's API key and uses **TokenSaver**, a built-in set of tools, to read files, run commands, read the screen, use the browser, and work with Office files on your computer, while keeping the API cost as low as possible.

## Requirements

- Windows 10/11, 64-bit
- Microsoft Edge or Google Chrome (for the app window)
- An API key from the owner

## Install

1. Download the zip from the latest [release](https://github.com/Qcenaman/APIChat/releases) and unzip it (or take `dist\apichat.exe` from this repo).
2. Double-click `apichat.exe`. The first run copies it to `C:\Claude Apps\APIChat` (or `%LOCALAPPDATA%\APIChat`), creates `Open APIChat.cmd` there, and opens the app window.
3. The Settings panel appears on first start. Paste the **API key** and click **Save**.

From then on, open it with **Open APIChat.cmd** or the copied `apichat.exe`. New versions: run the new `apichat.exe` once; it replaces the old copy and keeps your settings, chats and key.

Keep the small console window open while you use the app; closing it stops APIChat.

**If Windows Defender or SmartScreen complains:** the exe is not code-signed, so Defender's machine-learning check sometimes flags it as "potentially unwanted". Choose **More info → Run anyway**, or add the install folder (`C:\Claude Apps\APIChat`) as an exclusion in Windows Security → Virus & threat protection → Exclusions. If Defender already removed the file, restore it from Protection history, then add the exclusion and run `apichat.exe` again.

## Using it

- Type a message and press **Enter** (Shift+Enter for a new line).
- Attach files with the paperclip, by dragging them in, or by pasting an image. Files that are not images are saved on your PC and read through TokenSaver, which is much cheaper than sending them in full. Tick **send inline** on a file if you want Claude to receive the whole thing.
- The model picker at the top chooses the model per chat. **Auto** picks the cheapest model that fits each request.
- The bar at the bottom left shows how much of the monthly credit has been spent. **Usage** shows the breakdown.
- Some actions (clicking in other apps, running commands, deleting files, and so on) show an **Allow / Deny** card in the chat first.
- If a reply says the clipboard was changed, a **Restore clipboard** button puts your old clipboard back.

## What Claude can do on this PC

Everything goes through TokenSaver, which runs locally:

| Area | What it does |
|---|---|
| Files | find, scan, read parts of files (text, Office, PDF, images via OCR), copy, move, rename, delete to the Recycle Bin, write and edit text files, download, extract archives, undo |
| Commands | run cmd or PowerShell commands as background jobs |
| Screen | read on-screen text of any window, click, type, scroll, screenshots (limited), clipboard |
| Browser | its own Chrome profile for JavaScript or signed-in pages: open, read, click, fill |
| Web | search and read pages as text (free, no API cost) |
| Office | create, edit, read and convert Word, Excel, PowerPoint and PDF files |
| Media | transcribe audio and video, extract frames |
| Local AI | an optional local model to summarize or sort many files without API cost |
| Memory | search your files by meaning and keep notes across chats |

With a **guest profile** (set by the owner or in Settings), file access is limited to the folders listed under *Guest folders*, admin commands are off, and risky actions always ask first.

## Settings worth knowing

| Setting | Meaning |
|---|---|
| Profile | `owner` has no limits; `guest` is limited to the listed folders |
| Guest folders | one folder per line that Claude may read and change |
| Default model / Auto | which models to use |
| API web search | off by default; TokenSaver's own web tool is free. Turn on only if you need the API's search at $0.01 per search |
| Screenshots per turn | caps real screenshots, since every image costs tokens |
| TokenSaver folder | where TokenSaver keeps jobs, cache and its config |
| TokenSaver config.json | local AI endpoint and model, blocked paths, etc. |

Settings live in `C:\Claude Apps\APIChat\data\config.json`; chats, uploads and the usage log are in the same `data` folder.

## Costs

The owner's plan includes a fixed amount of API credit each month. APIChat tracks every call and shows the running total. When the credit runs out, requests fail until the next month. To keep costs down: use Auto, keep **send inline** off for big files, prefer window text over screenshots, and let the local AI do bulk reading when it is set up.

## Local AI (optional)

TokenSaver can use a model running on this PC for `local_ai`, `web ask`, `rag` and screenshot questions. Point it at an OpenAI-compatible server on localhost (llama.cpp, Ollama or LM Studio) in **Settings → TokenSaver config.json**:

```json
{ "ai_endpoint": "http://127.0.0.1:11434/v1", "ai_model": "qwen3:8b" }
```

Without it, those tools simply report that no local model is available; everything else works.

## Remote assistance (optional)

The owner can help on your PC from theirs, with your permission each time. It works over [Tailscale](https://tailscale.com), a private network between the two computers, so nothing is open to the internet.

1. Install Tailscale on this PC (`winget install tailscale.tailscale`, or the installer from tailscale.com) and sign in with the account the owner tells you to use.
2. In APIChat **Settings → Remote assistance**, tick **Allow assistance**, set the **Assist password** the owner gave you, Save, and restart APIChat. The console window shows `assist: listening on 100.x.x.x:47822`; tell the owner that address.
3. When the owner connects, a popup asks whether you accept help for a set number of minutes. Nothing runs before you click **Accept**.
4. While a session is active, a yellow bar shows who is helping and how many commands ran. **Log** shows every command and result; **End assistance** stops it immediately. Actions that need confirmation under your profile still ask you first.

## Updating

Download the new zip, unzip, and run the new `apichat.exe` once. Settings, chats and the key are kept.

## Building from source

Requires Go 1.24+:

```
go build -trimpath -ldflags "-s -w" -o apichat.exe .
```

To refresh the embedded TokenSaver from its repo: `go run ./tools/syncts <path to TokenSaver checkout>`, then rebuild.
