import platform
import subprocess
from typing import Dict, List

from src.file import FileModule


class ExecutionResult:
    def __init__(self, action: str, params: dict, result: str):
        self.action = action
        self.params = params
        self.result = result

    def to_dict(self) -> dict:
        return {
            "action": self.action,
            "params": self.params,
            "result": self.result,
        }


class CommandModule:
    FILE_ACTIONS = {
        "list_dir", "make_dir", "delete_dir", "rename_dir",
        "read_file", "write_file", "delete_file", "edit_file", "rename_file",
    }

    FORBIDDEN_PATTERNS = ["rm -rf /"]

    def __init__(self, controller, file_module: FileModule):
        self._controller = controller
        self._file_module = file_module
        self._model = None

    def set_model(self, model):
        self._model = model

    def check_safety(self, cmd: str) -> bool:
        cmd_lower = cmd.lower()
        for pattern in self.FORBIDDEN_PATTERNS:
            if pattern.lower() in cmd_lower:
                return False
        return True

    def parse_and_execute(self, commands: list) -> List[ExecutionResult]:
        results = []
        for cmd in commands:
            action = cmd.get("action")
            params = {k: v for k, v in cmd.items() if k != "action"}

            if action == "exec_cmd" and not self.check_safety(params.get("command", "")):
                results.append(ExecutionResult(
                    action, params, "Error: Command blocked due to safety concerns"
                ))
                continue

            if self._controller.get_auth_mode() == 0:
                auth_result = self._model.request_auth([cmd])
                if not auth_result.authorized:
                    results.append(ExecutionResult(
                        action, params, "Error: User denied command execution"
                    ))
                    continue

            result = self._execute_action(action, params)
            results.append(ExecutionResult(action, params, result))

        return results

    def execute(self, action: str, params: dict) -> str:
        if action == "exec_cmd" and not self.check_safety(params.get("command", "")):
            return "Error: Command blocked due to safety concerns"
        return self._execute_action(action, params)

    def _execute_action(self, action: str, params: dict) -> str:
        if action in self.FILE_ACTIONS:
            return self._execute_file_action(action, params)
        elif action == "exec_cmd":
            return self._execute_cmd(params.get("command", ""))
        return f"Error: Unknown action: {action}"

    def _execute_file_action(self, action: str, params: dict) -> str:
        action_map = {
            "list_dir": lambda: self._file_module.list_dir(params.get("path", ".")),
            "make_dir": lambda: self._file_module.create_dir(params.get("path", "")),
            "delete_dir": lambda: self._file_module.delete(params.get("path", "")),
            "rename_dir": lambda: self._file_module.rename(
                params.get("path", ""), params.get("new_name", "")
            ),
            "read_file": lambda: self._file_module.read_file(
                params.get("path", ""),
                params.get("start_line", 0),
                params.get("end_line", 0),
            ),
            "write_file": lambda: self._file_module.write_file(
                params.get("path", ""), params.get("content", "")
            ),
            "delete_file": lambda: self._file_module.delete(params.get("path", "")),
            "edit_file": lambda: self._file_module.edit_file(
                params.get("path", ""),
                params.get("operation", ""),
                params.get("start_line", 0),
                params.get("end_line", 0),
                params.get("content", ""),
            ),
            "rename_file": lambda: self._file_module.rename(
                params.get("path", ""), params.get("new_name", "")
            ),
        }
        handler = action_map.get(action)
        if handler:
            return handler()
        return f"Error: Unknown file action: {action}"

    def _execute_cmd(self, cmd: str) -> str:
        if not cmd:
            return "Error: Empty command"
        try:
            encoding = "cp936" if platform.system() == "Windows" else "utf-8"
            timeout = self._controller.get_config().cmd_timeout
            result = subprocess.run(
                cmd,
                shell=True,
                capture_output=True,
                text=True,
                encoding=encoding,
                errors="replace",
                timeout=timeout,
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
            return f"Error: Command timed out after {timeout} seconds"
        except Exception as e:
            return f"Error: {str(e)}"
