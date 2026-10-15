CREATE PROCEDURE acme.tricky_strings
AS
BEGIN
    SELECT N'line one;
GO
END; BEGIN' AS note;
    SELECT 'it''s; -- not a comment' AS quoted, '/* nor this */' AS block;
END
GO
