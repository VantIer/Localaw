import os
import platform
import shutil
import subprocess
from pathlib import Path
from typing import Any, Dict, List


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

    def execute_read_file(
        self, path: str, start_line: int = 0, end_line: int = 0
    ) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory"
            with open(target, "r", encoding="utf-8") as f:
                lines = f.readlines()
            if start_line == 0 or start_line == "" or start_line is None:
                return "".join(lines)[:50000]
            start = max(0, start_line - 1)
            end = min(len(lines), end_line)
            if start >= len(lines):
                return f"Start line {start_line} exceeds file line count ({len(lines)})"
            return "".join(lines[start:end])
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

    def execute_make_dir(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if target.exists():
                return f"Directory already exists: {path}"
            target.mkdir(parents=True, exist_ok=True)
            return f"Successfully created directory: {path}"
        except Exception as e:
            return f"Error creating directory: {str(e)}"

    def execute_delete_dir(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            if not target.is_dir():
                return f"{path} is not a directory"
            shutil.rmtree(target)
            return f"Successfully deleted directory: {path}"
        except Exception as e:
            return f"Error deleting directory: {str(e)}"

    def execute_rename_dir(self, path: str, new_name: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Directory does not exist: {path}"
            if not target.is_dir():
                return f"{path} is not a directory"
            new_path = target.parent / new_name
            if new_path.exists():
                return f"Target name already exists: {new_name}"
            target.rename(new_path)
            return f"Successfully renamed directory: {path} -> {new_name}"
        except Exception as e:
            return f"Error renaming directory: {str(e)}"

    def execute_edit_file(
        self,
        path: str,
        operation: str,
        start_line: int,
        end_line: int,
        content: str = "",
    ) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory"
            with open(target, "r", encoding="utf-8") as f:
                lines = f.readlines()
            if operation == "add":
                insert_pos = max(0, start_line - 1)
                lines.insert(insert_pos, content + "\n")
            elif operation == "del":
                start = max(0, start_line - 1)
                end = min(len(lines), end_line)
                if start >= len(lines):
                    return f"Start line {start_line} exceeds file line count ({len(lines)})"
                del lines[start:end]
            elif operation == "modify":
                start = max(0, start_line - 1)
                end = min(len(lines), end_line)
                if start >= len(lines):
                    return f"Start line {start_line} exceeds file line count ({len(lines)})"
                del lines[start:end]
                insert_pos = start
                lines.insert(insert_pos, content + "\n")
            else:
                return f"Unknown operation: {operation}. Use 'add', 'del', or 'modify'"
            with open(target, "w", encoding="utf-8") as f:
                f.writelines(lines)
            return f"Successfully performed {operation} on file: {path}"
        except Exception as e:
            return f"Error editing file: {str(e)}"

    def execute_rename_file(self, path: str, new_name: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory, use rename_dir instead"
            new_path = target.parent / new_name
            if new_path.exists():
                return f"Target name already exists: {new_name}"
            target.rename(new_path)
            return f"Successfully renamed file: {path} -> {new_name}"
        except Exception as e:
            return f"Error renaming file: {str(e)}"

    def execute_cmd(self, cmd: str) -> str:
        if not self.check_safety(cmd):
            return "Command blocked due to safety concerns"
        try:
            encoding = "cp936" if platform.system() == "Windows" else "utf-8"
            result = subprocess.run(
                cmd,
                shell=True,
                capture_output=True,
                text=True,
                encoding=encoding,
                errors="replace",
                timeout=60,
            )
            output = []
            if result.stdout:
                output.append(result.stdout)
            if result.stderr:
                output.append(result.stderr)
            if result.returncode != 0 and not output:
                output.append(f"Exit code: {result.returncode}")
            return (
                "\n".join(output)
                if output
                else "Command executed successfully (no output)"
            )
        except subprocess.TimeoutExpired:
            return "Command timed out after 60 seconds"
        except Exception as e:
            return f"Error executing command: {str(e)}"

    def execute(self, action: str, params: Dict[str, Any]) -> str:
        action_map = {
            "list_dir": lambda: self.execute_list_dir(params.get("path", ".")),
            "make_dir": lambda: self.execute_make_dir(params.get("path", "")),
            "delete_dir": lambda: self.execute_delete_dir(params.get("path", "")),
            "rename_dir": lambda: self.execute_rename_dir(
                params.get("path", ""), params.get("new_name", "")
            ),
            "read_file": lambda: self.execute_read_file(
                params.get("path", ""),
                params.get("start_line", 0),
                params.get("end_line", 0),
            ),
            "write_file": lambda: self.execute_write_file(
                params.get("path", ""), params.get("content", "")
            ),
            "delete_file": lambda: self.execute_delete_file(params.get("path", "")),
            "edit_file": lambda: self.execute_edit_file(
                params.get("path", ""),
                params.get("operation", ""),
                params.get("start_line", 0),
                params.get("end_line", 0),
                params.get("content", ""),
            ),
            "rename_file": lambda: self.execute_rename_file(
                params.get("path", ""), params.get("new_name", "")
            ),
            "exec_cmd": lambda: self.execute_cmd(params.get("command", "")),
        }
        handler = action_map.get(action)
        if handler:
            return handler()
        return f"Unknown action: {action}"
