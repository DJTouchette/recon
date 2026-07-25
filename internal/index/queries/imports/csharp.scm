; C# using directives and namespace declarations.
;
; @path captures the dotted namespace of a using directive (qualified,
; single-identifier and using-static forms).
;
; @ns captures the namespace a file *declares*, in both the file-scoped
; (`namespace A.B;`) and block (`namespace A.B { }`) forms. Resolution matches
; usings against declared namespaces; matching them against directory names
; instead fabricates edges to any directory that happens to end in the same
; word.
(using_directive (qualified_name) @path)
(using_directive (identifier) @path)

(file_scoped_namespace_declaration name: (_) @ns)
(namespace_declaration name: (_) @ns)
