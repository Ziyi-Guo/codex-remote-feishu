import unittest
from profc_memory.evidence import complete_quote, quote_options


class EvidenceTests(unittest.TestCase):
    def test_docx_block_and_inline_markup_preserve_sentence_meaning(self):
        source = '<title>讨论</title><p>尚未<b>验收成功</b>。</p><ul><li>仅支持官方 Key。</li></ul>'
        self.assertFalse(complete_quote('验收成功', source))
        self.assertTrue(complete_quote('尚未<b>验收成功</b>。', source))
        self.assertTrue(complete_quote('仅支持官方 Key。', source))

    def test_table_cell_is_a_complete_visible_block(self):
        self.assertTrue(complete_quote('需要确认平台信息', '<table><tr><td><p>需要确认平台信息</p></td></tr></table>'))

    def test_selectable_quotes_preserve_source_units_and_negation(self):
        options = quote_options('<p>尚未<b>验收成功</b>。</p><p>仍然需要提供正式的平台信息。</p>')
        self.assertIn('尚未<b>验收成功</b>。', options)
        self.assertNotIn('验收成功', options)
        self.assertTrue(all(complete_quote(q, '<p>尚未<b>验收成功</b>。</p><p>仍然需要提供正式的平台信息。</p>') for q in options))
