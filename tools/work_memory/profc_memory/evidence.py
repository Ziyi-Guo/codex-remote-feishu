"""Evidence checks shared by extraction and the canonical write boundary."""

import re
from html.parser import HTMLParser


class _VisibleText(HTMLParser):
    blocks = {'p', 'li', 'td', 'th', 'tr', 'table', 'title', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'br'}

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.parts = []

    def handle_starttag(self, tag, attrs):
        if tag in self.blocks:
            self.parts.append('\n')

    def handle_endtag(self, tag):
        if tag in self.blocks:
            self.parts.append('\n')

    def handle_data(self, data):
        self.parts.append(data)


def visible_text(content):
    parser = _VisibleText()
    parser.feed(content)
    return ''.join(parser.parts)


def complete_quote(quote, content):
    if not isinstance(quote, str) or not quote.strip():
        return False
    if quote not in content:
        return False
    if re.search(r'<(?:p|li|td|th|title|h[1-6])(?:\s[^>]*)?>', content):
        quote, content = visible_text(quote).strip(), visible_text(content)
    if not quote:
        return False
    boundaries = '。！？.!?;；\n'
    for match in re.finditer(re.escape(quote), content):
        before = content[:match.start()].rstrip(' \t\r')
        after = content[match.end():].lstrip(' \t\r')
        if ((not before or before[-1] in boundaries)
                and (not after or after.startswith('\n') or quote.rstrip()[-1] in boundaries)):
            return True
    return False


def quote_options(content):
    """Constrain the decoder to exact complete source units, rather than retyped prose."""
    if re.search(r'<(?:p|li|td|th)(?:\s[^>]*)?>', content):
        blocks = [m[2] for m in re.finditer(r'<(p|li|td|th)(?:\s[^>]*)?>(.*?)</\1>', content, re.S)]
    else:
        blocks = content.splitlines()
    choices = []
    for block in blocks:
        units = [block.strip()] if len(block.strip()) <= 300 else re.findall(r'[^。！？!?]+[。！？!?]', block)
        for unit in units:
            unit = unit.strip()
            if unit and len(unit) <= 300 and complete_quote(unit, content) and unit not in choices:
                choices.append(unit)
    return choices
