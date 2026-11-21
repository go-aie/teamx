"""Tokenize recorded shell text; never execute commands or inspect the host."""
import json
import shlex
import sys

result = []
for command in json.load(sys.stdin):
    # DSH accepts escaped outer quotes in a model bash command. Normalize this
    # spelling before lexical accounting; quoted JSON/report bodies stay args.
    command = command.replace('\\"', '"')
    lexer = shlex.shlex(command, posix=True, punctuation_chars=';&|()\n')
    lexer.whitespace = ' \t\r'
    lexer.whitespace_split = True
    lexer.commenters = ''
    tokens = list(lexer)
    groups, current = [], []
    for token in tokens:
        if token and all(c in ';&|()\n' for c in token):
            if current:
                groups.append(current)
                current = []
        else:
            current.append(token)
    if current:
        groups.append(current)
    result.append(groups)
json.dump(result, sys.stdout, ensure_ascii=False)
