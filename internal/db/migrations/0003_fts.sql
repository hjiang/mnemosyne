CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    subject,
    from_addr,
    to_addrs,
    cc_addrs,
    body_text,
    content='',
    contentless_delete=1
);
