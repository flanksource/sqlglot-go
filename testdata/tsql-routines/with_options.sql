CREATE PROCEDURE acme.with_options
    @order_id INT
WITH EXECUTE AS OWNER, RECOMPILE
AS
BEGIN
    SET NOCOUNT ON;
    SELECT order_id FROM acme.orders WHERE order_id = @order_id;
END
