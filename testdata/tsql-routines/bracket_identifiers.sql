CREATE PROCEDURE [acme].[rename_customer]
    @customer_id INT,
    @new_name NVARCHAR(100)
AS
BEGIN
    UPDATE [acme].[customers]
    SET [display name] = @new_name, [name] = @new_name
    WHERE [customer_id] = @customer_id;

    SELECT [c].[customer_id], [c].[display name] AS [Display Name]
    FROM [acme].[customers] AS [c]
    WHERE [c].[customer_id] = @customer_id;
END
