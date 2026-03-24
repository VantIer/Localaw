import sys
import os
import platform

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


def main():
    tool = Localaw()

    print("=" * 60)
    print("Localaw - Local AI Assistant")
    print("=" * 60)
    print(f"OS: {tool.system_name}")
    print(f"Model: {tool.config.model}")
    print(f"API Base: {tool.config.api_base}")
    print("=" * 60)
    print("\nCommands:")
    print("  :auth on    - Enable session authorization mode")
    print("  :auth off   - Disable session authorization mode")
    print("  :reset      - Reset conversation")
    print("  :quit       - Exit")
    print("  :config     - Show current configuration")
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
                continue

            llm_response, commands = tool.process_user_input(user_input)

            print(f"\nAI: {llm_response}")

            if commands:
                print("\n" + "-" * 40)
                print("Commands detected:")
                for i, cmd in enumerate(commands, 1):
                    print(f"  {i}. {cmd.get('action')} - {cmd}")

                if tool.need_authorization():
                    print("\nAuthorization required!")
                    print("  :y - Execute all commands")
                    print("  :n - Skip execution")
                    print("  :y-all - Execute and enable session auth")

                    auth = input("\nExecute? (:y/:n/:y-all) ").strip().lower()
                    if auth == ":y-all":
                        tool.set_auth_mode(AuthMode.SESSION)
                        results = tool.execute_commands(commands, True)
                    elif auth == ":y":
                        results = tool.execute_commands(commands, True)
                    else:
                        results = {"executions": [], "skipped": True}
                        try:
                            denial_response = tool.llm.send_message("User denied command execution")
                            print(f"\nAI: {denial_response}")
                        except Exception as e:
                            print(f"\nAI acknowledged the denial.")
                else:
                    print("\n(Session authorized - executing automatically)")
                    results = tool.execute_commands(commands, True)

                if results.get("executions"):
                    print("\n" + "-" * 40)
                    print("Execution Results:")
                    for ex in results["executions"]:
                        print(f"\n[{ex['action']}]")
                        print(f"  Result: {ex['result'][:500]}")

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
