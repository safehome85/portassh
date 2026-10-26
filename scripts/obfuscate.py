#!/usr/bin/env python3
"""Обфускация чувствительных строк в baked.go (XOR с плавающим ключом).

Режимы:
  obfuscate.py <baked.go>           — зашифровать литералы in place
  obfuscate.py <baked.go> --deobf   — вернуть открытый текст (для редактирования)

Защищаемые имена: defaultRelay, relayPrivKeyB64, relayHostKeyLine,
serverHostKeyB64, allowedClientPassword. Открытые клиентские ключи
(allowedClientPubKeys) не трогаем — это публичные данные.
mustDeobf() не может жить в const-блоке: блок с ней становится var.
"""
import base64
import os
import re
import sys

TARGETS = ["defaultRelay", "relayPrivKeyB64", "relayHostKeyLine",
           "serverHostKeyB64", "allowedClientPassword"]


def xor_str(key: bytes, data: str) -> str:
    raw = data.encode()
    enc = bytes(b ^ key[i % len(key)] for i, b in enumerate(raw))
    return base64.b64encode(enc).decode()


def obfuscate(src: str) -> str:
    for name in TARGETS:
        pat = re.compile(rf'{name}\s+= "(.*?)"\n')
        m = pat.search(src)
        if not m:
            continue
        key = os.urandom(16)
        repl = (f'{name} = mustDeobf("{xor_str(key, m.group(1))}", '
                f'"{base64.b64encode(key).decode()}")\n')
        src = pat.sub(repl.replace("\\", "\\\\"), src, count=1)
    # блоки const, содержащие mustDeof/mustDeobf, становятся var
    def const_to_var(mm: re.Match) -> str:
        body = mm.group(1)
        return ("var (" + body + ")") if "mustDeobf(" in body else mm.group(0)
    return re.sub(r'const \((.*?)\n\)', const_to_var, src, flags=re.S)


def deobfuscate(src: str) -> str:
    pat = re.compile(r'(\w+) = mustDeobf\("(.*?)", "(.*?)"\)\n')
    def repl(m: re.Match) -> str:
        enc = base64.b64decode(m.group(2))
        key = base64.b64decode(m.group(3))
        plain = bytes(b ^ key[i % len(key)] for i, b in enumerate(enc)).decode()
        return f'{m.group(1)} = "{plain}"\n'
    return pat.sub(repl, src)


def main() -> None:
    if len(sys.argv) < 2:
        sys.exit("usage: obfuscate.py <baked.go> [--deobf]")
    path = sys.argv[1]
    src = open(path, encoding="utf-8").read()
    src = deobfuscate(src) if "--deobf" in sys.argv else obfuscate(src)
    open(path, "w", encoding="utf-8").write(src)


if __name__ == "__main__":
    main()
