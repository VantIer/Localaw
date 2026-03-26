# Localaw

一个连接远程 LLM API 并在本地系统执行命令的 AI 助手。

## 功能特点

- 连接 OpenAI 兼容的 LLM API（Ollama、vLLM 等）
- 根据 AI 响应在本地系统执行命令
- 授权模式：每次询问或会话授权
- CLI 和 Web 界面
- 文件操作和命令执行
- 多轮对话：AI 可根据执行结果自动继续执行（最多 20 轮）
- Web 界面：FileManager 文件管理器、Command 命令执行面板

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
    "round_limit": 20,
    "listen_host": "127.0.0.1",
    "listen_port": 8880
}
```

**配置项说明：**
- `api_base`：LLM API 地址
- `api_key`：API 密钥
- `model`：模型名称
- `round_limit`：多轮对话最大轮数，默认 20
- `listen_host`：Web 服务监听地址
- `listen_port`：Web 服务监听端口

## 使用方法

### CLI 模式

```bash
python -m src.main
# 或
python -m src.main --mode cli
```

### Web 模式

```bash
python -m src.main --mode web
```

然后在浏览器中打开 http://127.0.0.1:8880

### Web 界面功能

- **Controls 控制面板**：主题切换、授权模式设置、会话重置
- **Command 命令面板**：直接输入 shell 命令执行
- **FileManager 文件管理器**：
  - 浏览目录（单击选中，双击进入目录）
  - 新建文件、删除文件/目录
  - 上传和下载文件

点击标题栏右侧按钮可打开对应面板，同时只能打开一个面板。

![主界面](./Docs/web_main.png)
![Controls 面板](./Docs/web_ctrl.png)
![Command 面板](./Docs/web_cmd.png)
![FileManager](./Docs/web_file.png)

### 自定义配置

使用 `--config` 指定配置文件路径（必须为绝对路径）：

```bash
# Python 模块运行
python -m src.main --config /path/to/config.json

# 打包后的可执行文件
Localaw.exe --mode web --config D:\MyConfigs\localaw.json
```

### 启动脚本

Windows:
```bash
start.bat
```

Linux/Mac:
```bash
bash start.sh
```

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
- Linux：已测试

**AI 提供商：**
- DeepSeek：已测试
- Minimax：已测试
- 其他提供商（OpenAI、Ollama 等）：未测试

**界面：**
- Web 模式：已测试
- CLI 模式：已测试
