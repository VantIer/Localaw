import sys
import os
import json
import asyncio
from pathlib import Path

if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
    base_path = sys._MEIPASS
else:
    base_path = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, base_path)

from fastapi import FastAPI, Request, Form, UploadFile, File
from fastapi.responses import HTMLResponse, JSONResponse, FileResponse, StreamingResponse
import uvicorn

from src.controller import Controller
from src.file import FileModule
from src.command import CommandModule
from src.model import ModelModule


class WebServer:
    def __init__(self, config_path: str = "config.json"):
        self._controller = Controller(config_path)
        self._file_module = FileModule()
        self._command = CommandModule(self._controller, self._file_module)
        self._model = ModelModule(self._controller, self._command, mode="web")
        self._command.set_model(self._model)
        self._current_path = Path.cwd()

        if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
            self._web_dir = Path(sys._MEIPASS) / "web"
        else:
            self._web_dir = Path(__file__).parent.parent / "web"

        self._app = FastAPI()
        self._setup_routes()

    def _setup_routes(self):
        @self._app.get("/")
        async def home():
            html_file = self._web_dir / "index.html"
            return FileResponse(str(html_file))

        @self._app.get("/api/config")
        async def get_config():
            return {
                "api_base": self._controller.get_config().api_base,
                "model": self._controller.get_config().model,
                "auth_mode": self._controller.get_auth_mode(),
            }

        # ---- Chat (SSE streaming) ----

        @self._app.post("/api/chat-stream")
        async def chat_stream(message: str = Form(...)):
            async def event_generator():
                async for event in self._model.chat_stream(message):
                    yield f"data: {json.dumps(event)}\n\n"
            return StreamingResponse(event_generator(), media_type="text/event-stream")

        # ---- Auth callback ----

        @self._app.post("/api/authorize-execute")
        async def authorize_execute(authorized: str = Form(...), commands: str = Form(...)):
            try:
                is_authorized = authorized.lower() == "true"
                cmd_list = json.loads(commands) if commands else []
                self._model.submit_web_auth(is_authorized, cmd_list)
                return JSONResponse({"success": True})
            except Exception as e:
                self._model.submit_web_auth(False, [])
                return JSONResponse({"success": False, "error": str(e)})

        @self._app.post("/api/stop")
        async def stop_conversation():
            self._model.stop()
            return JSONResponse({"success": True})

        # ---- Auth mode ----

        @self._app.post("/api/set-auth")
        async def set_auth(mode: str = Form(...)):
            try:
                auth_mode_val = int(mode)
                self._controller.set_auth_mode(auth_mode_val)
                return {"success": True, "auth_mode": self._controller.get_auth_mode()}
            except ValueError:
                return {"success": False, "error": "Invalid auth mode"}

        # ---- Session ----

        @self._app.post("/api/reset")
        async def reset():
            self._model.reset_conversation()
            return {"success": True}

        @self._app.get("/api/history")
        async def get_history():
            return {"history": self._model.get_history()}

        # ---- Direct command (Command Panel) ----

        @self._app.post("/api/exec-cmd")
        async def exec_cmd(command: str = Form(...)):
            try:
                result = self._command.execute("exec_cmd", {"command": command})
                return JSONResponse({"result": result, "error": None})
            except Exception as e:
                return JSONResponse({"result": None, "error": str(e)})

        # ---- File management ----

        @self._app.get("/api/files/list")
        async def list_files():
            try:
                items = []
                for item in self._current_path.iterdir():
                    items.append({
                        "name": item.name,
                        "is_dir": item.is_dir(),
                        "size": item.stat().st_size if item.is_file() else 0,
                    })
                return JSONResponse({
                    "current_path": str(self._current_path),
                    "items": items,
                    "error": None,
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/parent")
        async def parent_dir():
            try:
                if self._current_path.parent != self._current_path:
                    self._current_path = self._current_path.parent
                return JSONResponse({
                    "current_path": str(self._current_path),
                    "error": None,
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/chdir")
        async def chdir(dirname: str = Form(...)):
            try:
                if Path(dirname).is_absolute():
                    target = Path(dirname)
                else:
                    target = self._current_path / dirname
                if not target.exists() or not target.is_dir():
                    return JSONResponse({"error": "Directory not found"}, status_code=404)
                self._current_path = target.resolve()
                return JSONResponse({
                    "current_path": str(self._current_path),
                    "error": None,
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/new")
        async def new_file(filename: str = Form(...)):
            try:
                file_path = self._current_path / filename
                file_path.touch()
                return JSONResponse({"path": str(file_path), "error": None})
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/delete")
        async def delete_file(filepath: str = Form(...)):
            try:
                target = Path(filepath)
                if target.exists():
                    result = self._file_module.delete(filepath)
                    return JSONResponse({"success": True, "result": result, "error": None})
                return JSONResponse({"error": "File not found"}, status_code=404)
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.get("/api/files/download")
        async def download_file(path: str = None):
            try:
                if not path:
                    return JSONResponse({"error": "Path required"}, status_code=400)
                file_path = Path(path)
                if not file_path.exists() or not file_path.is_file():
                    return JSONResponse({"error": "File not found"}, status_code=404)
                return FileResponse(str(file_path), filename=file_path.name)
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/upload")
        async def upload_file(file: UploadFile = File(...)):
            try:
                content = await file.read()
                result = self._file_module.upload(
                    str(self._current_path), file.filename, content
                )
                return JSONResponse({
                    "path": str(self._current_path / file.filename),
                    "filename": file.filename,
                    "result": result,
                    "error": None,
                })
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/mkdir")
        async def make_dir(dirname: str = Form(...)):
            try:
                dir_path = self._current_path / dirname
                result = self._file_module.create_dir(str(dir_path))
                return JSONResponse({"path": str(dir_path), "result": result, "error": None})
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/copy")
        async def copy_file(src: str = Form(...), dest: str = Form(...)):
            try:
                result = self._file_module.copy(src, dest)
                return JSONResponse({"src": src, "dest": dest, "result": result, "error": None})
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

        @self._app.post("/api/files/move")
        async def move_file(src: str = Form(...), dest: str = Form(...)):
            try:
                result = self._file_module.move(src, dest)
                return JSONResponse({"src": src, "dest": dest, "result": result, "error": None})
            except Exception as e:
                return JSONResponse({"error": str(e)}, status_code=500)

    def run(self):
        config = self._controller.get_config()
        uvicorn.run(self._app, host=config.listen_host, port=config.listen_port, log_level="info")


def main():
    import argparse
    parser = argparse.ArgumentParser(description="Localaw Web Server")
    parser.add_argument("--config", default="config.json", help="Path to config file")
    args = parser.parse_args()

    server = WebServer(args.config)
    print(f"Starting Localaw Web Server...")
    print(f"Open http://{server._controller.get_config().listen_host}:{server._controller.get_config().listen_port} in your browser")
    server.run()


if __name__ == "__main__":
    main()
