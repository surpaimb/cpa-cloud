namespace CPACloud.Launcher;

internal sealed class PasswordDialog : Form
{
    private readonly TextBox _password = new() { UseSystemPasswordChar = true, Dock = DockStyle.Fill };
    private readonly TextBox _confirmation = new() { UseSystemPasswordChar = true, Dock = DockStyle.Fill };
    private readonly Label _validation = new()
    {
        AutoSize = true,
        ForeColor = Color.Firebrick,
        Dock = DockStyle.Fill,
    };

    internal PasswordDialog()
    {
        Text = "初始化 CPA Cloud";
        StartPosition = FormStartPosition.CenterScreen;
        FormBorderStyle = FormBorderStyle.FixedDialog;
        MinimizeBox = false;
        MaximizeBox = false;
        ShowInTaskbar = true;
        ClientSize = new Size(430, 215);
        AutoScaleMode = AutoScaleMode.Dpi;

        var description = new Label
        {
            Text = "请设置首位管理员密码。密码仅通过本机子进程标准输入传递，不会写入启动器日志。",
            AutoSize = true,
            MaximumSize = new Size(400, 0),
            Dock = DockStyle.Fill,
        };

        var ok = new Button { Text = "初始化", AutoSize = true };
        var cancel = new Button { Text = "取消", AutoSize = true, DialogResult = DialogResult.Cancel };
        ok.Click += ValidateAndClose;

        var buttons = new FlowLayoutPanel
        {
            Dock = DockStyle.Fill,
            FlowDirection = FlowDirection.RightToLeft,
            AutoSize = true,
        };
        buttons.Controls.Add(cancel);
        buttons.Controls.Add(ok);

        var layout = new TableLayoutPanel
        {
            Dock = DockStyle.Fill,
            Padding = new Padding(14),
            ColumnCount = 2,
            RowCount = 5,
        };
        layout.ColumnStyles.Add(new ColumnStyle(SizeType.AutoSize));
        layout.ColumnStyles.Add(new ColumnStyle(SizeType.Percent, 100));
        layout.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        layout.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        layout.RowStyles.Add(new RowStyle(SizeType.AutoSize));
        layout.RowStyles.Add(new RowStyle(SizeType.Percent, 100));
        layout.RowStyles.Add(new RowStyle(SizeType.AutoSize));

        layout.Controls.Add(description, 0, 0);
        layout.SetColumnSpan(description, 2);
        layout.Controls.Add(new Label { Text = "密码：", AutoSize = true, Anchor = AnchorStyles.Left }, 0, 1);
        layout.Controls.Add(_password, 1, 1);
        layout.Controls.Add(new Label { Text = "确认：", AutoSize = true, Anchor = AnchorStyles.Left }, 0, 2);
        layout.Controls.Add(_confirmation, 1, 2);
        layout.Controls.Add(_validation, 0, 3);
        layout.SetColumnSpan(_validation, 2);
        layout.Controls.Add(buttons, 0, 4);
        layout.SetColumnSpan(buttons, 2);
        Controls.Add(layout);

        AcceptButton = ok;
        CancelButton = cancel;
        Shown += (_, _) => _password.Focus();
    }

    internal string Password => _password.Text;

    private void ValidateAndClose(object? sender, EventArgs args)
    {
        var error = PasswordValidator.Validate(_password.Text, _confirmation.Text);
        if (error is not null)
        {
            _validation.Text = error;
            _password.SelectAll();
            _password.Focus();
            return;
        }

        DialogResult = DialogResult.OK;
        Close();
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            _password.Clear();
            _confirmation.Clear();
        }
        base.Dispose(disposing);
    }
}
