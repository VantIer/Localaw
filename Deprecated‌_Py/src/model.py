import asyncio
import threading
import time
from typing import List, Optional

from src.llm import LLMClient, CommandParser
from src.command import ExecutionResult


class ChatResult:
    def __init__(self, response=None, error=None):
        self.response = response or ""
        self.error = error


class AuthResult:
    def __init__(self, authorized=False):
        self.authorized = authorized


class ModelModule:
    FORBIDDEN_PATTERNS = ["rm -rf /"]

    def __init__(self, controller, command_module, mode="cli"):
        self._controller = controller
        self._command = command_module
        self._mode = mode
        config = self._controller.get_config()
        self._llm = LLMClient(config.api_base, config.api_key)
        self._conversation_history: List[dict] = []

        # Web auth callback state
        self._web_auth_event: Optional[threading.Event] = None
        self._web_auth_result: Optional[AuthResult] = None

        # Stop request state
        self._stop_lock = threading.Lock()
        self._stop_requested = False

    # ----------------------------------------------------------------
    # CLI mode: synchronous chat
    # ----------------------------------------------------------------
    def chat(self, message: str) -> ChatResult:
        self._set_stop(False)
        self._conversation_history.append({"role": "user", "content": message})

        iteration = 0
        last_response = ""

        while iteration < self._get_max_iterations():
            iteration += 1
            messages = [{"role": "system", "content": self._controller.get_config().system_prompt}]
            messages.extend(self._conversation_history)

            try:
                stream = self._llm.chat(
                    messages=messages,
                    model=self._controller.get_config().model,
                    stream=True,
                )

                full_response = ""
                for chunk in stream:
                    if chunk.choices and chunk.choices[0].delta.content:
                        content = chunk.choices[0].delta.content
                        full_response += content
                        print(content, end="", flush=True)
                print()

                if full_response:
                    self._conversation_history.append({"role": "assistant", "content": full_response})
                    last_response = full_response

                parsed_commands, parse_errors = CommandParser.parse(full_response)

                if parse_errors:
                    error_text = "\n".join(parse_errors)
                    print(f"\n[Parse Error] {error_text}")
                    self._conversation_history.append(
                        {"role": "user", "content": f"JSON parse error occurred. Please fix the JSON format and resend:\n{error_text}"}
                    )

                if not parsed_commands:
                    if not parse_errors:
                        return ChatResult(response=last_response)
                    continue

                # Process commands serially
                all_results = []
                user_denied = False

                for cmd in parsed_commands:
                    action = cmd.get("action")
                    params = {k: v for k, v in cmd.items() if k != "action"}

                    # Safety check
                    if action == "exec_cmd" and not self.check_safety(params.get("command", "")):
                        all_results.append(ExecutionResult(
                            action, params, "Error: Command blocked due to safety concerns"
                        ))
                        continue

                    # Auth check
                    if self._requires_auth(action):
                        # 授权申请触发时清零轮数，保证中途开启自动授权后仍有完整轮次预算
                        iteration = 0
                        auth_result = self.request_auth([cmd])
                        if not auth_result.authorized:
                            all_results.append(ExecutionResult(
                                action, params, "Error: User denied command execution"
                            ))
                            user_denied = True
                            break

                    result_str = self._command.execute(action, params)
                    all_results.append(ExecutionResult(action, params, result_str))

                result_text = "\n".join([self._format_result(r) for r in all_results])
                self._conversation_history.append(
                    {"role": "user", "content": f"Command execution result:\n{result_text}"}
                )

                if user_denied:
                    break

            except Exception as e:
                return ChatResult(error=str(e), response=last_response)

        return ChatResult(response=last_response)

    # ----------------------------------------------------------------
    # Web mode: async SSE streaming
    # ----------------------------------------------------------------
    async def chat_stream(self, message: str):
        self._set_stop(False)
        self._conversation_history.append({"role": "user", "content": message})

        iteration = 0
        turn = 0
        stopped = False

        while iteration < self._get_max_iterations():
            if self._is_stop_requested():
                stopped = True
                break

            iteration += 1
            turn += 1
            yield {"type": "answering", "iteration": turn}

            messages = [{"role": "system", "content": self._controller.get_config().system_prompt}]
            messages.extend(self._conversation_history)

            full_response = ""
            commands = []

            try:
                stream = self._llm.chat(
                    messages=messages,
                    model=self._controller.get_config().model,
                    stream=True,
                )

                for chunk in stream:
                    if chunk.choices and chunk.choices[0].delta.content:
                        content = chunk.choices[0].delta.content
                        full_response += content
                        yield {"type": "chunk", "content": content}

                self._conversation_history.append({"role": "assistant", "content": full_response})

                if self._is_stop_requested():
                    stopped = True
                    break

                parsed_commands, parse_errors = CommandParser.parse(full_response)

                if parse_errors:
                    error_text = "\n".join(parse_errors)
                    yield {"type": "parse_error", "errors": parse_errors}
                    self._conversation_history.append(
                        {"role": "user", "content": f"JSON parse error occurred. Please fix the JSON format and resend:\n{error_text}"}
                    )

                if parsed_commands:
                    commands = parsed_commands

                yield {"type": "response_done", "iteration": iteration, "commands": commands}

                if not commands:
                    if not parse_errors:
                        break
                    continue

                # Process commands serially
                all_results = []
                user_denied = False

                for cmd in commands:
                    if self._is_stop_requested():
                        stopped = True
                        break

                    action = cmd.get("action")
                    params = {k: v for k, v in cmd.items() if k != "action"}

                    # Safety check
                    if action == "exec_cmd" and not self.check_safety(params.get("command", "")):
                        result = ExecutionResult(action, params, "Error: Command blocked due to safety concerns")
                        all_results.append(result)
                        yield {"type": "execution_done", "results": [result.to_dict()]}
                        continue

                    # Auth check
                    if self._requires_auth(action):
                        # 授权申请触发时清零轮数（仅影响 round_limit 循环计数）
                        iteration = 0
                        yield {"type": "auth_required", "commands": [cmd]}
                        yield {"type": "waiting_auth", "iteration": turn}

                        # Wait for web auth callback
                        self._web_auth_event = threading.Event()
                        self._web_auth_result = None

                        # Run blocking wait in thread pool
                        auth_authorized = await asyncio.to_thread(self._wait_web_auth)

                        if self._is_stop_requested():
                            stopped = True
                            break

                        if not auth_authorized:
                            yield {"type": "auth_denied", "message": "User denied command execution"}
                            user_denied = True
                            break

                    # Execute
                    yield {"type": "executing", "commands": [cmd]}
                    result_str = await asyncio.to_thread(self._command.execute, action, params)
                    result = ExecutionResult(action, params, result_str)
                    all_results.append(result)
                    yield {"type": "execution_done", "results": [result.to_dict()]}

                if stopped:
                    break

                if user_denied:
                    self._conversation_history.append(
                        {"role": "user", "content": "User denied command execution"}
                    )
                    break

                result_text = "\n".join([self._format_result(r) for r in all_results])
                self._conversation_history.append(
                    {"role": "user", "content": f"Command execution result:\n{result_text}"}
                )

            except Exception as e:
                yield {"type": "error", "error": str(e)}
                break

        if stopped:
            yield {"type": "stopped", "iteration": turn}
        yield {"type": "done", "iteration": turn}

    # ----------------------------------------------------------------
    # Auth interfaces
    # ----------------------------------------------------------------
    def request_auth(self, commands: list) -> AuthResult:
        if self._mode == "cli":
            return self._request_auth_cli(commands)
        return AuthResult(authorized=False)

    def _request_auth_cli(self, commands: list) -> AuthResult:
        print("\n" + "=" * 50)
        print("Commands detected:")
        for cmd in commands:
            action = cmd.get("action")
            params = {k: v for k, v in cmd.items() if k != "action"}
            if action == "exec_cmd":
                print(f"  {action}: {params.get('command', '')}")
            elif action == "write_file":
                print(f"  {action}: {params.get('path', '')}")
            else:
                print(f"  {action}: {params}")

        print("\n" + "-" * 50)
        print("Authorization options:")
        print("  /y            - Allow current command")
        print("  /n            - Deny current command")
        print("  /auth 0|1|2   - Switch mode, then re-evaluate current command")
        print("-" * 50)

        while True:
            auth = input("\nYour choice: ").strip().lower()

            if auth == "/y":
                return AuthResult(authorized=True)
            elif auth == "/n":
                return AuthResult(authorized=False)
            elif auth.startswith("/auth"):
                parts = auth.split()
                if len(parts) == 2 and parts[1] in ("0", "1", "2"):
                    self._controller.set_auth_mode(int(parts[1]))
                    if not any(
                        self._requires_auth(c.get("action")) for c in commands
                    ):
                        return AuthResult(authorized=True)
                    print(
                        "Authorization still required for the current command. "
                        "Please choose /y, /n, or /auth 0|1|2."
                    )
                else:
                    print("Invalid mode. Usage: /auth 0|1|2")
            else:
                print("Unknown command. Valid options: /y, /n, /auth 0|1|2")

    def submit_web_auth(self, authorized: bool):
        self._web_auth_result = AuthResult(authorized=authorized)
        if self._web_auth_event:
            self._web_auth_event.set()

    def _wait_web_auth(self) -> bool:
        if not self._web_auth_event:
            return False
        deadline = time.time() + 300
        while not self._web_auth_event.is_set():
            if self._is_stop_requested():
                return False
            remaining = deadline - time.time()
            if remaining <= 0:
                break
            self._web_auth_event.wait(timeout=min(1.0, remaining))
        if self._web_auth_event.is_set() and self._web_auth_result:
            return self._web_auth_result.authorized
        return False

    def stop(self):
        self._set_stop(True)

    def _is_stop_requested(self) -> bool:
        with self._stop_lock:
            return self._stop_requested

    def _set_stop(self, requested: bool):
        with self._stop_lock:
            self._stop_requested = requested

    # ----------------------------------------------------------------
    # Command analysis
    # ----------------------------------------------------------------
    def check_safety(self, cmd: str) -> bool:
        cmd_lower = cmd.lower()
        for pattern in self.FORBIDDEN_PATTERNS:
            if pattern.lower() in cmd_lower:
                return False
        return True

    def _requires_auth(self, action: str) -> bool:
        """根据当前授权模式与指令风险等级判断是否需要申请授权。
        0=需要授权，1=低风险自动授权(高风险需授权)，2=自动授权"""
        mode = self._controller.get_auth_mode()
        if mode == 0:
            return True
        if mode == 1:
            return self._command.is_high_risk(action)
        return False

    # ----------------------------------------------------------------
    # Common helpers
    # ----------------------------------------------------------------
    def _get_max_iterations(self) -> int:
        """多轮轮数限制：恒定受 round_limit 约束。
        触发授权时会立即将轮数计数器清零，从而在授权后获得完整轮次预算。"""
        return self._controller.get_config().round_limit

    def reset_conversation(self):
        self._conversation_history = []
        self._controller.reset_auth()
        self._set_stop(False)

    def get_history(self) -> list:
        return self._conversation_history

    @staticmethod
    def _format_result(ex: ExecutionResult) -> str:
        params = ex.params
        detail = params.get("command", "") if ex.action == "exec_cmd" else params.get("path", "")
        return f"[{ex.action}] [{detail}]\n{ex.result}"
