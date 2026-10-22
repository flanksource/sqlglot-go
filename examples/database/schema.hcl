schema "public" {}

table "items" {
  schema = schema.public
  column "id" {
    type = integer
  }
  column "label" {
    type = text
    null = true
  }
  primary_key {
    columns = [column.id]
  }
}
