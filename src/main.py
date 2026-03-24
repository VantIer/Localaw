import sys
import os
import platform
import json

if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
    base_path = sys._MEIPASS
else:
    base_path = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, base_path)

from src.config import Config
from src.llm import LLMClient, CommandParser
from src.executor import CommandExecutor


class AuthMode:
    ALWAYS = "always"
    SESSION = "session"


def get_system_name():
    system = platform.system().lower()
    if system == "windows":
        return "Windows"
    elif system == "linux":
        return "Linux"
    elif system == "darwin":
        return "macOS"
    return system


class Localaw:
    def __init__(self, config_path: str = "config.json"):
        self.config = Config(config_path)
        
        self.system_name = get_system_name()
        self.config.system_prompt = self.config.system_prompt \
            .replace("{system_name}", self.system_name)
        
        self.llm = LLMClient(self.config)
        self.executor = CommandExecutor()
        self.auth_mode = AuthMode.ALWAYS
        self.session_authorized = False

    def set_auth_mode(self, mode: str):
        self.auth_mode = mode
        if mode == AuthMode.SESSION:
            self.session_authorized = True
        else:
            self.session_authorized = False

    def reset_session_auth(self):
        self.session_authorized = False

    def process_user_input(self, user_input: str) -> tuple[str, list]:
        llm_response = self.llm.send_message(user_input)
        commands = CommandParser.parse(llm_response)
        return llm_response, commands or []

    def execute_commands(self, commands: list, auto_authorized: bool = False) -> dict:
        results = []
        for cmd in commands:
            action = cmd.get("action")
            params = {k: v for k, v in cmd.items() if k != "action"}
            result = self.executor.execute(action, params)
            results.append({
                "action": action,
                "params": params,
                "result": result
            })
        return {"executions": results}

    def need_authorization(self) -> bool:
        if self.auth_mode == AuthMode.ALWAYS:
            return True
        return not self.session_authorized

    def format_command_result(self, ex):
        action = ex["action"]
        params = ex["params"]
        if action == "exec_cmd":
            cmd_str = params.get("command", "")
            return f"[{action}] [{cmd_str}]\n{ex['result']}"
        elif action == "write_file":
            path = params.get("path", "")
            return f"[{action}] [{path}]\n{ex['result']}"
        else:
            path = params.get("path", "")
            return f"[{action}] [{path}]\n{ex['result']}"

    def ask_authorization(self, commands: list) -> tuple[bool, list]:
        print("\n" + "=" * 50)
        print("Commands detected:")
        for i, cmd in enumerate(commands, 1):
            action = cmd.get("action")
            params = {k: v for k, v in cmd.items() if k != "action"}
            if action == "exec_cmd":
                print(f"  {i}. {action}: {params.get('command', '')}")
            elif action == "write_file":
                print(f"  {i}. {action}: {params.get('path', '')}")
            else:
                print(f"  {i}. {action}: {params}")

        print("\n" + "-" * 50)
        print("Authorization options:")
        print("  :y        - Execute ALL commands")
        print("  :n        - Skip ALL commands (deny)")
        print("  :s 1,2,3  - Select specific commands to execute")
        print("  :y-all    - Execute all and enable session auth")
        print("  :q        - Quit execution")
        print("-" * 50)

        while True:
            auth = input("\nYour choice: ").strip().lower()
            
            if auth == ":y":
                return True, commands
            elif auth == ":n":
                return False, []
            elif auth == ":y-all":
                self.set_auth_mode(AuthMode.SESSION)
                return True, commands
            elif auth.startswith(":s "):
                try:
                    indices = [int(x.strip()) - 1 for x in auth[3:].split(",")]
                    selected = [commands[i] for i in indices if 0 <= i < len(commands)]
                    if selected:
                        return True, selected
                    else:
                        print("No valid commands selected. Try again.")
                except ValueError:
                    print("Invalid format. Use :s 1,2,3")
            elif auth == ":q":
                return False, None
            else:
                print("Unknown command. Valid options: :y, :n, :s 1,2,3, :y-all, :q")


def main():
    tool = Localaw()

    print("=" * 60)
    print("Localaw - Local AI Assistant (Multi-turn)")
    print("=" * 60)
    print(f"OS: {tool.system_name}")
    print(f"Model: {tool.config.model}")
    print(f"API Base: {tool.config.api_base}")
    print("=" * 60)
    print("\nCommands:")
    print("  :auth on    - Enable session authorization mode")
    print("  :auth off   - Disable session authorization mode (always ask)")
    print("  :reset      - Reset conversation")
    print("  :quit       - Exit")
    print("  :config     - Show current configuration")
    print("\n")
    print("Note: Multi-turn conversation is enabled. After command")
    print("      execution, results will be fed back to AI for")
    print("      further processing (max 20 iterations).")
    print("\n")

    while True:
        try:
            user_input = input("\nYou: ").strip()
            if not user_input:
                continue

            if user_input.lower() in [":quit", ":q", "exit"]:
                print("Goodbye!")
                break

            if user_input.lower() == ":reset":
                tool.llm.reset_conversation()
                tool.session_authorized = False
                print("Conversation reset.")
                continue

            if user_input.lower() == ":auth on":
                tool.set_auth_mode(AuthMode.SESSION)
                print("Session authorization mode enabled.")
                continue

            if user_input.lower() == ":auth off":
                tool.auth_mode = AuthMode.ALWAYS
                print("Always ask for authorization mode.")
                continue

            if user_input.lower() == ":config":
                print(f"\nAPI Base: {tool.config.api_base}")
                print(f"Model: {tool.config.model}")
                print(f"Auth Mode: {tool.auth_mode}")
                print(f"Session Authorized: {tool.session_authorized}")
                continue

            tool.llm.conversation_history.append({"role": "user", "content": user_input})

            max_iterations = 20
            iteration = 0

            while iteration < max_iterations:
                iteration += 1

                messages = [{"role": "system", "content": tool.config.system_prompt}]
                messages.extend(tool.llm.conversation_history)

                try:
                    stream = tool.llm.client.chat.completions.create(
                        model=tool.config.model,
                        messages=messages,
                        temperature=0.7,
                        stream=True
                    )

                    full_response = ""
                    for chunk in stream:
                        if chunk.choices and chunk.choices[0].delta.content:
                            content = chunk.choices[0].delta.content
                            full_response += content

                    if full_response:
                        print(f"\nAI: {full_response}")
                        tool.llm.conversation_history.append({"role": "assistant", "content": full_response})

                    parsed_commands = CommandParser.parse(full_response) or []

                    if not parsed_commands:
                        break

                    need_auth = tool.need_authorization()
                    selected_commands = parsed_commands

                    if need_auth:
                        authorized, selected = tool.ask_authorization(parsed_commands)
                        if selected is None:
                            print("Stopping execution.")
                            break
                        if not authorized:
                            denial_response = tool.llm.send_message("User denied command execution")
                            print(f"\nAI: {denial_response}")
                            break
                        selected_commands = selected

                    print("\n" + "-" * 40)
                    print(f"Executing {len(selected_commands)} command(s)...")
                    results = tool.execute_commands(selected_commands)
                    tool.session_authorized = True

                    for ex in results.get("executions", []):
                        print(f"\n[{ex['action']}]")
                        print(f"  Result: {str(ex['result'])[:300]}")

                    result_text = "\n".join([tool.format_command_result(ex) for ex in results.get("executions", [])])
                    tool.llm.conversation_history.append({"role": "user", "content": f"Command execution result:\n{result_text}"})

                except Exception as e:
                    print(f"\nError in iteration {iteration}: {str(e)}")
                    break

            if iteration >= max_iterations:
                print(f"\nMax iterations ({max_iterations}) reached. Conversation continues...")

        except KeyboardInterrupt:
            print("\n\nInterrupted. Type :quit to exit.")
        except Exception as e:
            print(f"\nError: {str(e)}")


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Localaw - Local AI Assistant")
    parser.add_argument("--mode", choices=["cli", "web"], default="cli", help="Run mode: cli or web (default: cli)")
    parser.add_argument("--config", default="config.json", help="Path to config file")
    args = parser.parse_args()
    
    if args.mode == "web":
        from src.web_server import WebServer
        server = WebServer(args.config)
        print(f"Starting Localaw Web Server...")
        print(f"Open http://{server.config.listen_host}:{server.config.listen_port} in your browser")
        server.run()
    else:
        main()
