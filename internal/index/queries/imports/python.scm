; Python imports.
;
; @mod   the module of a from-import — relative (".", "..pkg") or absolute
;        ("app.core.engine"). Absolute imports are the recommended style and the
;        norm in src/-layout projects, so they must be captured; the resolver
;        only turns one into an edge when a file actually exists under a
;        detected source root.
; @name  a name imported by a from-import. `from . import mod_a` puts the whole
;        module in the name, so without this there is nothing to resolve.
; @plain the module of a plain `import a.b.c` / `import a.b as c`.

(import_from_statement module_name: (_) @mod)

(import_from_statement
  module_name: (_) @mod
  name: (dotted_name) @name)

(import_from_statement
  module_name: (_) @mod
  name: (aliased_import name: (dotted_name) @name))

(import_statement name: (dotted_name) @plain)

(import_statement name: (aliased_import name: (dotted_name) @plain))
