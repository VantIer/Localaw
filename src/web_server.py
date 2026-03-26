import sys
import os
import platform
import json
import asyncio
from pathlib import Path
from typing import Iterator

if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
    base_path = sys._MEIPASS
else:
    base_path = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, base_path)

from fastapi import FastAPI, Request, Form, HTTPException, UploadFile, File
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse, StreamingResponse
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

        self.auth_event = asyncio.Event()
        self.auth_result = None
        self.auth_commands = None
        self.auth_received = False
        self.current_path = Path.cwd()

        if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
            self.web_dir = Path(sys._MEIPASS) / "web"
        else:
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

        @self.app.post("/api/chat-stream")
        async def chat_stream(message: str = Form(...)):
            async def event_generator():
                self.auth_event.clear()
                self.auth_result = None
                self.auth_commands = None
                self.auth_received = False
                
                if self.auth_mode == AuthMode.ALWAYS:
                    self.session_authorized = False

                self.llm.conversation_history.append({"role": "user", "content": message})

                max_iterations = self.config.round_limit
                iteration = 0

                while iteration < max_iterations:
                    iteration += 1

                    yield f"data: {json.dumps({'type': 'answering', 'iteration': iteration})}\n\n"

                    messages = [{"role": "system", "content": self.config.system_prompt}]
                    messages.extend(self.llm.conversation_history)

                    full_response = ""
                    commands = []

                    try:
                        stream = self.llm.client.chat.completions.create(
                            model=self.config.model,
                            messages=messages,
                            temperature=0.7,
                            stream=True
                        )

                        for chunk in stream:
                            if chunk.choices and chunk.choices[0].delta.content:
                                content = chunk.choices[0].delta.content
                                full_response += content
                                yield f"data: {json.dumps({'type': 'chunk', 'content': content})}\n\n"

                        self.llm.conversation_history.append({"role": "assistant", "content": full_response})

                        parsed_commands = CommandParser.parse(full_response)
                        if parsed_commands:
                            commands = parsed_commands
                            self.current_commands = commands

                        yield f"data: {json.dumps({'type': 'response_done', 'iteration': iteration, 'commands': commands})}\n\n"

                        if not commands:
                            break

                        if self.need_authorization():
                            self.auth_commands = commands
                            yield f"data: {json.dumps({'type': 'auth_required', 'commands': commands})}\n\n"
                            yield f"data: {json.dumps({'type': 'waiting_auth', 'iteration': iteration})}\n\n"
                            
                            try:
                                await asyncio.wait_for(self.auth_event.wait(), timeout=300)
                            except asyncio.TimeoutError:
                                yield f"data: {json.dumps({'type': 'auth_denied', 'message': 'Authorization timeout'})}\n\n"
                                self.auth_event.clear()
                                self.auth_result = None
                                self.auth_commands = None
                                break

                            self.auth_received = True

                            if self.auth_result is None or self.auth_result.get('authorized') == False:
                                yield f"data: {json.dumps({'type': 'auth_denied', 'message': 'User denied command execution'})}\n\n"
                                self.auth_event.clear()
                                self.auth_result = None
                                self.auth_commands = None
                                break

                            selected_commands = self.auth_result.get('commands', commands)
                            if not selected_commands:
                                yield f"data: {json.dumps({'type': 'auth_denied', 'message': 'No commands selected'})}\n\n"
                                self.auth_event.clear()
                                self.auth_result = None
                                self.auth_commands = None
                                break

                            self.auth_event.clear()
                            self.auth_result = None

                            yield f"data: {json.dumps({'type': 'executing', 'commands': selected_commands})}\n\n"

                            results = self.execute_commands(selected_commands)
                            self.session_authorized = True

                            result_text = "\n".join([self.format_command_result(ex) for ex in results])
                            yield f"data: {json.dumps({'type': 'execution_done', 'results': results})}\n\n"

                            self.llm.conversation_history.append({"role": "user", "content": f"Command execution result:\n{result_text}"})
                            self.auth_commands = None
                            continue

                        yield f"data: {json.dumps({'type': 'executing', 'commands': commands})}\n\n"

                        results = self.execute_commands(commands)
                        self.session_authorized = True

                        result_text = "\n".join([self.format_command_result(ex) for ex in results])
                        yield f"data: {json.dumps({'type': 'execution_done', 'results': results})}\n\n"

                        self.llm.conversation_history.append({"role": "user", "content": f"Command execution result:\n{result_text}"})

                    except Exception as e:
                        yield f"data: {json.dumps({'type': 'error', 'error': str(e)})}\n\n"
                        break

                self.auth_commands = None
                yield f"data: {json.dumps({'type': 'done', 'iteration': iteration})}\n\n"

            return StreamingResponse(event_generator(), media_type="text/event-stream")

        @self.app.post("/api/execute")
        async def execute(authorize: bool = Form(...)):
            if not authorize:
                try:
                    follow_up = self.llm.send_message("User denied command execution")
                    self.current_commands = []
                    return JSONResponse({
                        "executions": [],
                        "response": follow_up,
                        "skipped": True
                    })
                except Exception as e:
                    self.current_commands = []
                    return JSONResponse({
                        "executions": [],
                        "response": "User denied command execution",
                        "skipped": True
                    })

            results = self.execute_commands(self.current_commands)
            self.session_authorized = True

            result_text = "\n".join([self.format_command_result(ex) for ex in results])

            try:
                follow_up = self.llm.send_message(f"Command execution result:\n{result_text}")
                self.current_commands = []
                return JSONResponse({
                    "executions": results,
                    "response": follow_up,
                    "skipped": False
                })
            except Exception as e:
                self.current_commands = []
                return JSONResponse({
                    "executions": results,
                    "response": f"Command executed, but failed to get AI response: {str(e)}",
                    "skipped": False
                })

        @self.app.post("/api/execute-single")
        async def execute_single(command: str = Form(...)):
            try:
                cmd = json.loads(command)
                action = cmd.get("action")
                params = {k: v for k, v in cmd.items() if k != "action"}
                result = self.executor.execute(action, params)
                self.session_authorized = True
                return JSONResponse({"result": result, "error": None})
            except Exception as e:
                return JSONResponse({"result": None, "error": str(e)})

        @self.app.post("/api/exec-cmd")
        async def exec_cmd(command: str = Form(...)):
            try:
                result = self.executor.execute("exec_cmd", {"command": command})
                self.session_authorized = True
                return JSONResponse({"result": result, "error": None})
            except Exception as e:
                return JSONResponse({"result": None, "error": str(e)})

        @self.app.post("/api/authorize-execute")
        async def authorize_execute(authorized: str = Form(...), commands: str = Form(...)):
            try:
                is_authorized = authorized.lower() == "true"
                cmd_list = json.loads(commands) if commands else []
                self.auth_result = {
                    "authorized": is_authorized,
                    "commands": cmd_list if is_authorized else []
                }
                self.auth_received = True
                self.auth_event.set()
                return JSONResponse({"success": True})
            except Exception as e:
                self.auth_result = {"authorized": False, "commands": []}
                self.auth_received = True
                self.auth_event.set()
                return JSONResponse({"success": False, "error": str(e)})

        @self.app.post("/api/continue")
        async def continue_conversation(execution_results: str = Form(...)):
            try:
                results = json.loads(execution_results)
                result_text = "\n".join([self.format_command_result(ex) for ex in results])
                self.llm.conversation_history.append({"role": "user", "content": f"Command execution result:\n{result_text}"})

                max_iterations = self.config.round_limit
                iteration = 0
                all_commands = []

                while iteration < max_iterations:
                    iteration += 1
                    messages = [{"role": "system", "content": self.config.system_prompt}]
                    messages.extend(self.llm.conversation_history)

                    full_response = self.llm.client.chat.completions.create(
                        model=self.config.model,
                        messages=messages,
                        temperature=0.7,
                        stream=False
                    ).choices[0].message.content

                    if full_response:
                        self.llm.conversation_history.append({"role": "assistant", "content": full_response})
                        parsed_commands = CommandParser.parse(full_response) or []
                    else:
                        parsed_commands = []
                    all_commands.extend(parsed_commands)

                    if not parsed_commands:
                        break

                    cmd_results = self.execute_commands(parsed_commands)
                    self.session_authorized = True

                    result_text = "\n".join([self.format_command_result(ex) for ex in cmd_results])
                    self.llm.conversation_history.append({"role": "user", "content": f"Command execution result:\n{result_text}"})

                return JSONResponse({
                    "response": full_response if full_response else "",
                    "commands": all_commands,
                    "iterations": iteration
                })
            except Exception as e:
                return JSONResponse({"response": f"Error: {str(e)}", "commands": [], "iterations": 0})

        @self.app.post("/api/set-auth")
        async def set_auth(mode: str = Form(...)):
            self.auth_mode = mode
            if mode == AuthMode.SESSION:
                self.session_authorized = True
            else:
                self.session_authorized = False
            return {"success": True, "auth_mode": mode}

        @self.app.post("/api/reset")
        async def reset():
            self.llm.reset_conversation()
            self.session_authorized = False
            self.auth_mode = AuthMode.ALWAYS
            self.current_commands = []
            return {"success": True}

        @self.app.get("/api/history")
        async def get_history():
            return {"history": self.llm.conversation_history}

        @self.app.get("/api/files/list")
        async def list_files():
            try:
                items = []
                for item in self.current_path.iterdir():
                    items.append({
                        "name": item.name,
                        "is_dir": item.is_dir(),
                        "size": item.stat().st_size if item.is_file() else 0
                    })
                return JSONResponse({
                    "current_path": str(self.current_path),
                    "items": items,
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/parent")
        async def parent_dir():
            try:
                if self.current_path.parent != self.current_path:
                    self.current_path = self.current_path.parent
                return JSONResponse({
                    "current_path": str(self.current_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/chdir")
        async def chdir(dirname: str = Form(...)):
            try:
                target = self.current_path / dirname
                if not target.exists() or not target.is_dir():
                    return JSONResponse({"error": "Directory not found"}, status_code=404)
                self.current_path = target.resolve()
                return JSONResponse({
                    "current_path": str(self.current_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/new")
        async def new_file(filename: str = Form(...)):
            try:
                file_path = self.current_path / filename
                file_path.touch()
                return JSONResponse({
                    "path": str(file_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/delete")
        async def delete_file(filepath: str = Form(...)):
            try:
                target = Path(filepath)
                if not target.is_relative_to(self.current_path):
                    return JSONResponse({"error": "Access denied"}, status_code=403)
                if target.exists():
                    if target.is_dir():
                        import shutil
                        shutil.rmtree(target)
                    else:
                        target.unlink()
                    return JSONResponse({"success": True, "error": None})
                return JSONResponse({"error": "File not found"}, status_code=404)
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.get("/api/files/download")
        async def download_file(path: str = None):
            try:
                if not path:
                    return JSONResponse({"error": "Path required"}, status_code=400)
                file_path = Path(path)
                if not file_path.is_relative_to(self.current_path):
                    return JSONResponse({"error": "Access denied"}, status_code=403)
                if not file_path.exists() or not file_path.is_file():
                    return JSONResponse({"error": "File not found"}, status_code=404)
                return FileResponse(str(file_path), filename=file_path.name)
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/upload")
        async def upload_file(file: UploadFile = File(...)):
            try:
                file_path = self.current_path / file.filename
                content = await file.read()
                with open(file_path, "wb") as f:
                    f.write(content)
                return JSONResponse({
                    "path": str(file_path),
                    "filename": file.filename,
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/mkdir")
        async def make_dir(dirname: str = Form(...)):
            try:
                dir_path = self.current_path / dirname
                dir_path.mkdir()
                return JSONResponse({
                    "path": str(dir_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/copy")
        async def copy_file(src: str = Form(...), dest: str = Form(...)):
            try:
                import shutil
                src_path = Path(src)
                dest_path = Path(dest)
                if not src_path.exists():
                    return JSONResponse({"error": "Source not found"}, status_code=404)
                if src_path.is_dir():
                    shutil.copytree(src_path, dest_path)
                else:
                    dest_path.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(src_path, dest_path)
                return JSONResponse({
                    "src": str(src_path),
                    "dest": str(dest_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self.app.post("/api/files/move")
        async def move_file(src: str = Form(...), dest: str = Form(...)):
            try:
                import shutil
                src_path = Path(src)
                dest_path = Path(dest)
                if not src_path.exists():
                    return JSONResponse({"error": "Source not found"}, status_code=404)
                dest_path.parent.mkdir(parents=True, exist_ok=True)
                shutil.move(str(src_path), str(dest_path))
                return JSONResponse({
                    "src": str(src_path),
                    "dest": str(dest_path),
                    "error": None
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

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