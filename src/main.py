import sys
import os

if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
    base_path = sys._MEIPASS
else:
    base_path = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, base_path)

from src.controller import Controller
from src.file import FileModule
from src.command import CommandModule
from src.model import ModelModule


def print_help():
    print("\nAvailable commands:")
    print("  /quit   - Exit the program")
    print("  /help   - Show this help message")
    print("  /reset  - Reset conversation and clear history")
    print("  /y-all  - Auto-authorize all subsequent commands")
    print("  /n-all  - Require authorization for subsequent commands")
    print("\nDuring authorization prompts:")
    print("  /y      - Allow the current command")
    print("  /n      - Deny the current command")
    print("  /y-all  - Allow and auto-authorize subsequent")
    print("  /n-all  - Deny and require authorization for subsequent")
    print()


def main(config_path: str = "config.json"):
    controller = Controller(config_path)
    file_module = FileModule()
    command_module = CommandModule(controller, file_module)
    model = ModelModule(controller, command_module, mode="cli")
    command_module.set_model(model)

    print("=" * 60)
    print("Localaw - Local AI Assistant (Multi-turn)")
    print("=" * 60)
    print(f"OS: {controller.system_name}")
    print(f"Model: {controller.get_config().model}")
    print(f"API Base: {controller.get_config().api_base}")
    print(f"Auth Mode: {'Auto-authorized' if controller.get_auth_mode() == 1 else 'Authorization required'}")
    print("=" * 60)
    print("\nCommands:")
    print("  /help   - Show help")
    print("  /quit   - Exit")
    print("  /reset  - Reset conversation")
    print("  /y-all  - Auto-authorize all commands")
    print("  /n-all  - Require authorization for all commands")
    print("\n")
    print("Note: Multi-turn conversation is enabled. After command")
    print("      execution, results will be fed back to AI for")
    print(f"      further processing (max {controller.get_config().round_limit} iterations).")
    print("\n")

    while True:
        try:
            user_input = input("\nYou: ").strip()
            if not user_input:
                continue

            if user_input.lower() in ["/quit", "/q"]:
                print("Goodbye!")
                break

            if user_input.lower() == "/help":
                print_help()
                continue

            if user_input.lower() == "/reset":
                model.reset_conversation()
                print("Conversation reset.")
                continue

            if user_input.lower() == "/n-all":
                controller.set_auth_mode(0)
                print("Authorization required for all commands.")
                continue

            if user_input.lower() == "/y-all":
                controller.set_auth_mode(1)
                print("Auto-authorization enabled for all commands.")
                continue

            result = model.chat(user_input)

            if result.error:
                print(f"\nError: {result.error}")
                print("You can try again or type a new message.")

        except KeyboardInterrupt:
            print("\n\nInterrupted. Type /quit to exit.")
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
        print(f"Open http://{server._controller.get_config().listen_host}:{server._controller.get_config().listen_port} in your browser")
        server.run()
    else:
        main(args.config)
