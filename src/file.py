import shutil
from pathlib import Path


class FileModule:
    def list_dir(self, path: str = ".") -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            if target.is_file():
                return f"{path} is a file"
            items = []
            for item in target.iterdir():
                item_type = "DIR" if item.is_dir() else "FILE"
                size = item.stat().st_size if item.is_file() else 0
                items.append(f"{item_type:6} {str(size):>12} {item.name}")
            return "\n".join(items) if items else "Empty directory"
        except Exception as e:
            return f"Error listing directory: {str(e)}"

    def create_file(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            target.parent.mkdir(parents=True, exist_ok=True)
            target.touch()
            return f"Successfully created file: {path}"
        except Exception as e:
            return f"Error creating file: {str(e)}"

    def create_dir(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if target.exists():
                return f"Directory already exists: {path}"
            target.mkdir(parents=True, exist_ok=True)
            return f"Successfully created directory: {path}"
        except Exception as e:
            return f"Error creating directory: {str(e)}"

    def delete(self, path: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            if target.is_dir():
                shutil.rmtree(target)
            else:
                target.unlink()
            return f"Successfully deleted: {path}"
        except Exception as e:
            return f"Error deleting: {str(e)}"

    def rename(self, path: str, new_name: str) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"Path does not exist: {path}"
            new_path = target.parent / new_name
            if new_path.exists():
                return f"Target name already exists: {new_name}"
            target.rename(new_path)
            return f"Successfully renamed: {path} -> {new_name}"
        except Exception as e:
            return f"Error renaming: {str(e)}"

    def read_file(self, path: str, start_line: int = 0, end_line: int = 0) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory"
            with open(target, "r", encoding="utf-8") as f:
                lines = f.readlines()
            if start_line == 0 or start_line == "" or start_line is None:
                return "".join(lines)[:50000]
            start = max(0, start_line - 1)
            end = min(len(lines), end_line)
            if start >= len(lines):
                return f"Start line {start_line} exceeds file line count ({len(lines)})"
            return "".join(lines[start:end])
        except Exception as e:
            return f"Error reading file: {str(e)}"

    def write_file(self, path: str, content: str) -> str:
        try:
            target = Path(path).resolve()
            target.parent.mkdir(parents=True, exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(content)
            return f"Successfully wrote to: {path}"
        except Exception as e:
            return f"Error writing file: {str(e)}"

    def edit_file(
        self,
        path: str,
        operation: str,
        start_line: int,
        end_line: int,
        content: str = "",
    ) -> str:
        try:
            target = Path(path).resolve()
            if not target.exists():
                return f"File does not exist: {path}"
            if target.is_dir():
                return f"{path} is a directory"
            with open(target, "r", encoding="utf-8") as f:
                lines = f.readlines()
            if operation == "add":
                insert_pos = max(0, start_line - 1)
                lines.insert(insert_pos, content + "\n")
            elif operation == "del":
                start = max(0, start_line - 1)
                end = min(len(lines), end_line)
                if start >= len(lines):
                    return f"Start line {start_line} exceeds file line count ({len(lines)})"
                del lines[start:end]
            elif operation == "modify":
                start = max(0, start_line - 1)
                end = min(len(lines), end_line)
                if start >= len(lines):
                    return f"Start line {start_line} exceeds file line count ({len(lines)})"
                del lines[start:end]
                lines.insert(start, content + "\n")
            else:
                return f"Unknown operation: {operation}. Use 'add', 'del', or 'modify'"
            with open(target, "w", encoding="utf-8") as f:
                f.writelines(lines)
            return f"Successfully performed {operation} on file: {path}"
        except Exception as e:
            return f"Error editing file: {str(e)}"

    def copy(self, src: str, dest: str) -> str:
        try:
            src_path = Path(src)
            dest_path = Path(dest)
            if not src_path.exists():
                return f"Source not found: {src}"
            if src_path.is_dir():
                shutil.copytree(src_path, dest_path)
            else:
                dest_path.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(src_path, dest_path)
            return f"Successfully copied: {src} -> {dest}"
        except Exception as e:
            return f"Error copying: {str(e)}"

    def move(self, src: str, dest: str) -> str:
        try:
            src_path = Path(src)
            dest_path = Path(dest)
            if not src_path.exists():
                return f"Source not found: {src}"
            dest_path.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(str(src_path), str(dest_path))
            return f"Successfully moved: {src} -> {dest}"
        except Exception as e:
            return f"Error moving: {str(e)}"

    def upload(self, base_path: str, filename: str, content: bytes) -> str:
        try:
            file_path = Path(base_path) / filename
            with open(file_path, "wb") as f:
                f.write(content)
            return f"Successfully uploaded: {filename}"
        except Exception as e:
            return f"Error uploading: {str(e)}"
