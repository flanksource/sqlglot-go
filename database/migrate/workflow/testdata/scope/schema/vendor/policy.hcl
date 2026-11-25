schema "dbo" {}

table "AsPolicy" {
  schema = schema.dbo
  column "PolicyGUID" {
    type = uniqueidentifier
  }
}
