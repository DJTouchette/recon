; Java import declarations. @path captures the dotted name (com.example.User).
; @star marks an on-demand (wildcard) import — `import com.example.models.*;`
; names a package, not a type, and resolves to every file in it.
(import_declaration (scoped_identifier) @path (asterisk) @star)
(import_declaration (scoped_identifier) @path)
(import_declaration (identifier) @path)
