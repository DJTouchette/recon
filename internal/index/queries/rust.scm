; Rust symbols. Functions in an impl/trait body are methods; elsewhere they are
; free functions.

(source_file (function_item name: (identifier) @function) @def)
(mod_item body: (declaration_list
  (function_item name: (identifier) @function) @def))

(impl_item body: (declaration_list
  (function_item name: (identifier) @method) @def))
(trait_item body: (declaration_list
  (function_item name: (identifier) @method) @def))

(struct_item name: (type_identifier) @struct) @def
(union_item name: (type_identifier) @struct) @def
(enum_item name: (type_identifier) @enum) @def
(trait_item name: (type_identifier) @trait) @def
(type_item name: (type_identifier) @type) @def

(const_item name: (identifier) @constant) @def
(static_item name: (identifier) @constant) @def
(mod_item name: (identifier) @module) @def
(macro_definition name: (identifier) @macro) @def
