# Atlas's project file for the declarative apply of schema.sql (D-63).
# `make migrate` and `toft upgrade` both pass
# --config file://<this file> --env toft.
#
# The schema only moves forward: Atlas never drops a schema, table, column
# or foreign key, even when it is handed an older schema.sql (deploying an
# older commit). A planned drop ("contract") is written by hand in
# contract.sql, one release after the code stopped using it. Constraints a
# newer schema added (CHECKs, indexes) can still be dropped by an older
# schema.sql; they hold no data.
env "toft" {
  diff {
    skip {
      drop_schema      = true
      drop_table       = true
      drop_column      = true
      drop_foreign_key = true
    }
  }
}
