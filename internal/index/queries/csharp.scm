; C# symbols.

(class_declaration name: (identifier) @class) @def
(interface_declaration name: (identifier) @interface) @def
(enum_declaration name: (identifier) @enum) @def
(struct_declaration name: (identifier) @struct) @def
(record_declaration name: (identifier) @class) @def

(method_declaration name: (identifier) @method) @def
(constructor_declaration name: (identifier) @method) @def
(property_declaration name: (identifier) @property) @def
(delegate_declaration name: (identifier) @delegate) @def

; Named constants: `const` and `static readonly` fields. This is where C# keeps SQL
; fragments, policy names and lookup tables, so a definition search for one must land
; on its declaration. Instance and mutable fields stay out: they are state, and
; indexing every `_field` would bury the names people actually look for.
(field_declaration
  (modifier) @_const
  (variable_declaration (variable_declarator name: (identifier) @constant))
  (#eq? @_const "const")) @def
(field_declaration
  (modifier) @_static
  (modifier) @_readonly
  (variable_declaration (variable_declarator name: (identifier) @constant))
  (#eq? @_static "static")
  (#eq? @_readonly "readonly")) @def

(namespace_declaration name: [(identifier) (qualified_name)] @module) @def
