import importlib.metadata

import eventsourcing

__version__ = "0.0.0"


def main() -> None:
    print(f"app_version={importlib.metadata.version('py-missing-dep')}")
    print(f"eventsourcing_version={importlib.metadata.version('eventsourcing')}")
    print(f"eventsourcing_module={eventsourcing.__name__}")
