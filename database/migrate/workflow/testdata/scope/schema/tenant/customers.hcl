schema "dbo" {}

table "OMCustomers" {
  schema = schema.dbo
  column "id" {
    type = uniqueidentifier
  }
}
