# Localaw

A local AI assistant that connects to remote LLM APIs and executes commands on your system.

[中文文档](./README_zh.md)

## Features

- Connect to OpenAI-compatible LLM APIs (Ollama, vLLM, etc.)
- Execute commands on local system based on AI responses
- Authorization modes: Always ask or Session-based
- CLI and Web interface
- File operations and command execution

## Setup

```bash
pip install -r requirements.txt
```

## Configuration

Edit `config.json`:

```json
{
    "api_base": "http://localhost:11434/v1",
    "api_key": "ollama",
    "model": "llama3.2",
    "listen_host": "127.0.0.1",
    "listen_port": 8880
}
```

## Usage

### CLI Mode

```bash
python -m src.main
```

### Web Mode

```bash
python -m src.web_server
```

Then open http://127.0.0.1:8880 in your browser.

## Supported Commands

The AI can request these commands:
- `list_dir` - List directory contents
- `read_file` - Read file contents
- `delete_file` - Delete files/directories
- `write_file` - Write files
- `exec_cmd` - Execute shell commands

## Disclaimer

**This tool is intended for personal local use only.**

- No authentication or security measures are implemented
- No input sanitization or command filtering is performed
- No remote access methods are provided or planned (except LAN web access)
- **DO NOT deploy in public, commercial, or production environments**
- If you deploy in such environments, you must understand what you are doing and take full responsibility for any issues

## Testing Status

**Platforms:**
- Windows: Tested
- Linux: Not tested

**AI Providers:**
- DeepSeek: Tested
- Other providers (OpenAI, Ollama, etc.): Not tested

**Interfaces:**
- Web Mode: Tested
- CLI Mode: Not tested
