import threading
import platform

from src.config import Config


class Controller:
    def __init__(self, config_path: str = "config.json"):
        self._config = Config(config_path)
        self._auth_mode = self._config.auth_mode
        self._auth_mode_initial = self._auth_mode
        self._lock = threading.Lock()
        self._init_system_info()

    def _init_system_info(self):
        system = platform.system().lower()
        if system == "windows":
            self._system_name = "Windows"
        elif system == "linux":
            self._system_name = "Linux"
        elif system == "darwin":
            self._system_name = "macOS"
        else:
            self._system_name = system
        self._config.system_prompt = self._config.system_prompt \
            .replace("{system_name}", self._system_name)

    @property
    def system_name(self) -> str:
        return self._system_name

    def get_auth_mode(self) -> int:
        with self._lock:
            return self._auth_mode

    def set_auth_mode(self, mode: int):
        with self._lock:
            self._auth_mode = mode

    def get_config(self) -> Config:
        return self._config

    def reset_auth(self):
        with self._lock:
            self._auth_mode = self._auth_mode_initial
