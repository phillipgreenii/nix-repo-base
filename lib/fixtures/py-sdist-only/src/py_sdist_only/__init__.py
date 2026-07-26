import importlib.metadata

import termcolor

__version__ = "0.0.0"


def main() -> None:
    # Prints regardless of argv so help2man's --help/--version probes exit 0.
    print(f"app_version={importlib.metadata.version('py-sdist-only')}")
    print(f"termcolor_version={importlib.metadata.version('termcolor')}")
    print(f"termcolor_module={termcolor.__name__}")
