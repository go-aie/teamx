"""Small standard-library Todo CLI, shared by the overlap experiment."""

import argparse
import json
from pathlib import Path


def load_tasks(path):
    if not path.exists():
        return {"version": 1, "next_id": 1, "tasks": []}
    return json.loads(path.read_text(encoding="utf-8"))


def save_tasks(path, data):
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def build_parser():
    parser = argparse.ArgumentParser(description="A small Todo list")
    parser.add_argument("--file", type=Path, default=Path("tasks.json"))
    commands = parser.add_subparsers(dest="command", required=True)
    add = commands.add_parser("add")
    add.add_argument("title")
    commands.add_parser("list")
    done = commands.add_parser("done")
    done.add_argument("id", type=int)
    return parser


def list_tasks(data, args):
    for task in sorted(data["tasks"], key=lambda task: task["id"]):
        mark = "x" if task["done"] else " "
        print(f'{task["id"]} [{mark}] {task["title"]}')


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    data = load_tasks(args.file)
    if args.command == "add":
        if not args.title.strip():
            parser.error("title must not be empty")
        task = {"id": data["next_id"], "title": args.title, "done": False}
        data["tasks"].append(task)
        data["next_id"] += 1
        save_tasks(args.file, data)
        print(task["id"])
    elif args.command == "list":
        list_tasks(data, args)
    elif args.command == "done":
        task = next((task for task in data["tasks"] if task["id"] == args.id), None)
        if task is None:
            parser.error(f"unknown task: {args.id}")
        task["done"] = True
        save_tasks(args.file, data)
        print(task["id"])


if __name__ == "__main__":
    main()
