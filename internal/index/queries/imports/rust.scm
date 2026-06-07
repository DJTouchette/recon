; Rust use-paths and mod declarations. @use captures the use path (the module
; prefix for grouped/glob imports), @mod the mod-declaration name.
(use_declaration (scoped_identifier) @use)
(use_declaration (scoped_use_list path: (_) @use))
(use_declaration (use_as_clause path: (scoped_identifier) @use))
(use_declaration (use_wildcard (scoped_identifier) @use))
(mod_item name: (identifier) @mod)
