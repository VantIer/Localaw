import asyncio
import json
import threading
from typing import Iterator, List, Optional

from src.llm import LLMClient, CommandParser
from src.command import ExecutionResult


class ChatResult:
    def __init__(self, response=None, commands=None, executions=None, error=None):
        self.response = response or ""
        self.commands = commands or []
        self.executions = executions or []
        self.error = error


class AuthResult:
    def __init__(self, authorized=False, commands=None):
        self.authorized = authorized
        self.commands = commands or []


class ModelModule:
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

    def set_mode(self, mode: str):
        self._mode = mode

    # ----------------------------------------------------------------
    # CLI mode: synchronous chat
    # ----------------------------------------------------------------
    def chat(self, message: str) -> ChatResult:
        self._conversation_history.append({"role": "user", "content": message})

        max_iterations = self._controller.get_config().round_limit
        iteration = 0
        last_response = ""

        while iteration < max_iterations:
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

                # Process commands serially via Command module
                results = self._command.parse_and_execute(parsed_commands)

                result_text = "\n".join([self._format_result(r) for r in results])
                self._conversation_history.append(
                    {"role": "user", "content": f"Command execution result:\n{result_text}"}
                )

            except Exception as e:
                return ChatResult(error=str(e), response=last_response)

        return ChatResult(response=last_response)

    # ----------------------------------------------------------------
    # Web mode: async SSE streaming
    # ----------------------------------------------------------------
    async def chat_stream(self, message: str):
        self._conversation_history.append({"role": "user", "content": message})

        max_iterations = self._controller.get_config().round_limit
        iteration = 0

        while iteration < max_iterations:
            iteration += 1
            yield {"type": "answering", "iteration": iteration}

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
                    action = cmd.get("action")
                    params = {k: v for k, v in cmd.items() if k != "action"}

                    # Safety check
                    if action == "exec_cmd" and not self._command.check_safety(params.get("command", "")):
                        result = ExecutionResult(action, params, "Error: Command blocked due to safety concerns")
                        all_results.append(result)
                        yield {"type": "execution_done", "results": [result.to_dict()]}
                        continue

                    # Auth check
                    if self._controller.get_auth_mode() == 0:
                        yield {"type": "auth_required", "commands": [cmd]}
                        yield {"type": "waiting_auth", "iteration": iteration}

                        # Wait for web auth callback
                        self._web_auth_event = threading.Event()
                        self._web_auth_result = None

                        # Run blocking wait in thread pool
                        auth_authorized = await asyncio.to_thread(self._wait_web_auth)

                        if not auth_authorized:
                            yield {"type": "auth_denied", "message": "User denied command execution"}
                            user_denied = True
                            break

                    # Execute
                    yield {"type": "executing", "commands": [cmd]}
                    result_str = self._command.execute(action, params)
                    result = ExecutionResult(action, params, result_str)
                    all_results.append(result)
                    yield {"type": "execution_done", "results": [result.to_dict()]}

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

        yield {"type": "done", "iteration": iteration}

    # ----------------------------------------------------------------
    # Auth interfaces
    # ----------------------------------------------------------------
    def request_auth(self, commands: list) -> AuthResult:
        if self._mode == "cli":
            return self._request_auth_cli(commands)
        return AuthResult(authorized=False, commands=[])

    def _request_auth_cli(self, commands: list) -> AuthResult:
        print("\n" + "=" * 50)
        print("Commands detected:")
        for i, cmd in enumerate(commands, 1):
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
        print("  /y      - Execute this command")
        print("  /n      - Deny this command")
        print("  /y-all  - Execute and auto-authorize subsequent")
        print("  /n-all  - Require authorization for subsequent")
        print("-" * 50)

        while True:
            auth = input("\nYour choice: ").strip().lower()

            if auth == "/y":
                return AuthResult(authorized=True, commands=commands)
            elif auth == "/n":
                return AuthResult(authorized=False, commands=[])
            elif auth == "/y-all":
                self._controller.set_auth_mode(1)
                return AuthResult(authorized=True, commands=commands)
            elif auth == "/n-all":
                self._controller.set_auth_mode(0)
                return AuthResult(authorized=False, commands=[])
            else:
                print("Unknown command. Valid options: /y, /n, /y-all, /n-all")

    def submit_web_auth(self, authorized: bool, commands: list):
        self._web_auth_result = AuthResult(authorized=authorized, commands=commands)
        if self._web_auth_event:
            self._web_auth_event.set()

    def _wait_web_auth(self) -> bool:
        if self._web_auth_event:
            self._web_auth_event.wait(timeout=300)
            if self._web_auth_event.is_set() and self._web_auth_result:
                return self._web_auth_result.authorized
        return False

    # ----------------------------------------------------------------
    # Common helpers
    # ----------------------------------------------------------------
    def reset_conversation(self):
        self._conversation_history = []
        self._controller.reset_auth()

    def get_history(self) -> list:
        return self._conversation_history

    @staticmethod
    def _format_result(ex: ExecutionResult) -> str:
        params = ex.params
        detail = params.get("command", "") if ex.action == "exec_cmd" else params.get("path", "")
        return f"[{ex.action}] [{detail}]\n{ex.result}"
