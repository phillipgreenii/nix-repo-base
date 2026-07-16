import importlib.metadata

import six

__version__ = "0.0.0"


def main() -> None:
    # Prints regardless of argv so help2man's --help/--version probes exit 0.
    print(f"app_version={importlib.metadata.version('py-lock-pin')}")
    print(f"app_dunder_version={__version__}")
    print(f"six_version={six.__version__}")
