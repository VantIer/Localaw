import os
import platform
import shutil
import subprocess
from pathlib import Path
from typing import Dict, Any, List


class CommandExecutor:
    def __init__(self):
        self.allowed_dirs = [Path.cwd()]
        self.forbidden_patterns = ["rm -rf /", "del /f /s /q c:", ":(){:|:&};:"]
        self.blocked_cmds = ["mkfs", "dd", "> /dev/", ">//dev/"]

    def check_safety(self, cmd: str) -> bool:
        cmd_lower = cmd.lower()
        for pattern in self.forbidden_patterns:
            if pattern.lower() in cmd_lower:
                return False
        for blocked in self.blocked_cmds:
            if blocked.lower() in cmd_lower:
                return False
        return True

    def execute_list_dir(self, path: str = ".") -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            if target.is_file():
                return f"{path} is a file"
            items = []
            for item in target.iterdir():
                item_type = "DIR" if item.is_dir() else "FILE"
                size = item.stat().st_size if item.is_file() else 0
                items.append(f"{item_type:6} {str(size):>12} {item.name}")
            return "\n".join(items) if items else "Empty directory"
        except Exception as e:
            return f"Error listing directory: {str(e)}"

    def execute_read_file(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory"
            with open(target, "r", encoding="utf-8") as f:
                content = f.read()
            return content[:50000]
        except Exception as e:
            return f"Error reading file: {str(e)}"

    def execute_delete_file(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            if target.is_dir():
                shutil.rmtree(target)
            else:
                target.unlink()
            return f"Successfully deleted: {path}"
        except Exception as e:
            return f"Error deleting: {str(e)}"

    def execute_write_file(self, path: str, content: str) -> str:
        try:
            target = Path(path).resolve()
            target.parent.mkdir(parents=True, exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(content)
            return f"Successfully wrote to: {path}"
        except Exception as e:
            return f"Error writing file: {str(e)}"

    def execute_cmd(self, cmd: str) -> str:
        if not self.check_safety(cmd):
            return "Command blocked due to safety concerns"
        try:
            encoding = 'cp936' if platform.system() == 'Windows' else 'utf-8'
            result = subprocess.run(
                cmd,
                shell=True,
                capture_output=True,
                text=True,
                encoding=encoding,
                errors='replace',
                timeout=60
            )
            output = []
            if result.stdout:
                output.append(result.stdout)
            if result.stderr:
                output.append(result.stderr)
            if result.returncode != 0 and not output:
                output.append(f"Exit code: {result.returncode}")
            return "\n".join(output) if output else "Command executed successfully (no output)"
        except subprocess.TimeoutExpired:
            return "Command timed out after 60 seconds"
        except Exception as e:
            return f"Error executing command: {str(e)}"

    def execute(self, action: str, params: Dict[str, Any]) -> str:
        action_map = {
            "list_dir": lambda: self.execute_list_dir(params.get("path", ".")),
            "read_file": lambda: self.execute_read_file(params.get("path", "")),
            "delete_file": lambda: self.execute_delete_file(params.get("path", "")),
            "write_file": lambda: self.execute_write_file(params.get("path", ""), params.get("content", "")),
            "exec_cmd": lambda: self.execute_cmd(params.get("command", "")),
        }
        handler = action_map.get(action)
        if handler:
            return handler()
        return f"Unknown action: {action}"
