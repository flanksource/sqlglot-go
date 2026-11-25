schema "dbo" {}

table "OMOrders" {
  schema = schema.dbo
  column "id" {
    type = uniqueidentifier
  }
}
