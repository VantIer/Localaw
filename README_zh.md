# Localaw

一个连接远程 LLM API 并在本地系统执行命令的 AI 助手。

## 功能特点

- 连接 OpenAI 兼容的 LLM API（Ollama、vLLM 等）
- 根据 AI 响应在本地系统执行命令
- 授权模式：每次询问或会话授权
- CLI 和 Web 界面
- 文件操作和命令执行

## 安装

```bash
pip install -r requirements.txt
```

## 配置

编辑 `config.json`：

```json
{
    "api_base": "http://localhost:11434/v1",
    "api_key": "ollama",
    "model": "llama3.2",
    "listen_host": "127.0.0.1",
    "listen_port": 8880
}
```

## 使用方法

### CLI 模式

```bash
python -m src.main
```

### Web 模式

```bash
python -m src.web_server
```

然后在浏览器中打开 http://127.0.0.1:8880

## 支持的命令

AI 可以请求执行以下命令：
- `list_dir` - 列出目录内容
- `read_file` - 读取文件内容
- `delete_file` - 删除文件/目录
- `write_file` - 写入文件
- `exec_cmd` - 执行 shell 命令

## 免责声明

**本工具仅供个人本地使用。**

- 未实现任何身份验证或安全措施
- 未进行任何输入过滤或命令过滤
- 不提供也不计划提供任何远程访问方式（局域网 Web 访问除外）
- **请勿在任何公开的、商业化的、生产环境等的环境中部署使用**
- 如果在前述环境中部署，请务必清楚自己在干什么，并且自行承担出现问题产生的责任

## 测试状态

**平台：**
- Windows：已测试
- Linux：未测试

**AI 提供商：**
- DeepSeek：已测试
- 其他提供商（OpenAI、Ollama 等）：未测试

**界面：**
- Web 模式：已测试
- CLI 模式：未测试
