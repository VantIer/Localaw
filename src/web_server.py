import sys
import os
import platform
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from fastapi import FastAPI, Request, Form, HTTPException
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse
import uvicorn

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


class WebServer:
    def __init__(self, config_path: str = "config.json"):
        self.config = Config(config_path)
        
        self.system_name = get_system_name()
        self.config.system_prompt = self.config.system_prompt \
            .replace("{system_name}", self.system_name)
        
        self.llm = LLMClient(self.config)
        self.executor = CommandExecutor()
        self.auth_mode = AuthMode.ALWAYS
        self.session_authorized = False
        self.pending_commands = []
        self.current_commands = []

        self.web_dir = Path(__file__).parent.parent / "web"
        self.app = FastAPI()
        self.setup_routes()

    def setup_routes(self):
        @self.app.get("/")
        async def home():
            html_file = self.web_dir / "index.html"
            return FileResponse(str(html_file))

        @self.app.get("/api/config")
        async def get_config():
            return {
                "api_base": self.config.api_base,
                "model": self.config.model,
                "auth_mode": self.auth_mode,
                "session_authorized": self.session_authorized
            }

        @self.app.post("/api/chat")
        async def chat(message: str = Form(...)):
            try:
                llm_response, commands = self.process_message(message)
                return JSONResponse({
                    "response": llm_response or "No response from AI",
                    "commands": commands or [],
                    "need_auth": bool(commands) and self.need_authorization(),
                    "auth_mode": self.auth_mode,
                    "session_authorized": self.session_authorized,
                    "error": None
                })
            except Exception as e:
                return JSONResponse({
                    "response": f"Error: {str(e)}",
                    "commands": [],
                    "need_auth": False,
                    "auth_mode": self.auth_mode,
                    "session_authorized": self.session_authorized,
                    "error": str(e)
                }, status_code=200)

        @self.app.post("/api/execute")
        async def execute(authorize: bool = Form(...)):
            if not authorize:
                return JSONResponse({"executions": [], "skipped": True})

            results = self.execute_commands(self.current_commands)
            if self.auth_mode == AuthMode.SESSION:
                self.session_authorized = True
            self.current_commands = []
            return JSONResponse({"executions": results})

        @self.app.post("/api/set-auth")
        async def set_auth(mode: str = Form(...)):
            self.auth_mode = mode
            if mode == AuthMode.SESSION:
                self.session_authorized = True
            return {"success": True, "auth_mode": mode}

        @self.app.post("/api/reset")
        async def reset():
            self.llm.reset_conversation()
            self.session_authorized = False
            self.current_commands = []
            return {"success": True}

        @self.app.get("/api/history")
        async def get_history():
            return {"history": self.llm.conversation_history}

    def process_message(self, message: str):
        llm_response = self.llm.send_message(message)
        commands = CommandParser.parse(llm_response) or []
        self.current_commands = commands
        return llm_response, commands

    def need_authorization(self):
        if self.auth_mode == AuthMode.ALWAYS:
            return True
        return not self.session_authorized

    def execute_commands(self, commands: list):
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
        return results

    def run(self):
        uvicorn.run(self.app, host=self.config.listen_host, port=self.config.listen_port, log_level="info")


def main():
    import argparse
    parser = argparse.ArgumentParser(description="Localaw Web Server")
    parser.add_argument("--config", default="config.json", help="Path to config file")
    args = parser.parse_args()

    server = WebServer(args.config)
    print(f"Starting Localaw Web Server...")
    print(f"Open http://{server.config.listen_host}:{server.config.listen_port} in your browser")
    server.run()


if __name__ == "__main__":
    main()
