import json
import os
import sys
from pathlib import Path
from typing import Optional


class Config:
    def __init__(self, config_path: str = "config.json"):
        if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
            base_dir = Path(sys._MEIPASS)
        else:
            base_dir = Path(__file__).parent.parent
        
        self.config_path = base_dir / config_path
        self.api_base: str = "http://localhost:11434/v1"
        self.api_key: str = "ollama"
        self.model: str = "llama3.2"
        self.round_limit: int = 20
        self.system_prompt: str = "You are a local AI assistant. When user asks you to perform actions, respond with JSON commands."
        self.listen_host: str = "127.0.0.1"
        self.listen_port: int = 8880
        self.load()

    def load(self):
        if self.config_path.exists():
            with open(self.config_path, "r", encoding="utf-8") as f:
                data = json.load(f)
                self.api_base = data.get("api_base", self.api_base)
                self.api_key = data.get("api_key", self.api_key)
                self.model = data.get("model", self.model)
                self.round_limit = data.get("round_limit", self.round_limit)
                self.system_prompt = data.get("system_prompt", self.system_prompt)
                self.listen_host = data.get("listen_host", self.listen_host)
                self.listen_port = data.get("listen_port", self.listen_port)

    def save(self):
        if getattr(sys, 'frozen', False) and hasattr(sys, '_MEIPASS'):
            base_dir = Path(sys._MEIPASS)
        else:
            base_dir = Path(__file__).parent.parent
        
        save_path = base_dir / self.config_path.name
        with open(save_path, "w", encoding="utf-8") as f:
            json.dump({
                "api_base": self.api_base,
                "api_key": self.api_key,
                "model": self.model,
                "round_limit": self.round_limit,
                "system_prompt": self.system_prompt,
                "listen_host": self.listen_host,
                "listen_port": self.listen_port
            }, f, indent=4, ensure_ascii=False)
