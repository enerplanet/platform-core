<#import "template.ftl" as layout>
<@layout.registrationLayout displayMessage=!messagesPerField.existsError('password','password-confirm'); section>
    <#if section = "form">
        <h2>Update Password</h2>
        <p>Please enter your new password below.</p>

        <form id="kc-passwd-update-form" action="${url.loginAction}" method="post">
            <input type="text" id="username" name="username" value="${username}" autocomplete="username" readonly="readonly" style="display:none;">

            <div class="form-group">
                <label for="password-new">New Password</label>
                <input type="password" id="password-new" name="password-new" autofocus autocomplete="new-password">
                <#if messagesPerField.existsError('password')>
                    <div class="alert alert-error" style="margin-top:6px">${kcSanitize(messagesPerField.getFirstError('password'))?no_esc}</div>
                </#if>
            </div>

            <div class="form-group">
                <label for="password-confirm">Confirm Password</label>
                <input type="password" id="password-confirm" name="password-confirm" autocomplete="new-password">
                <#if messagesPerField.existsError('password-confirm')>
                    <div class="alert alert-error" style="margin-top:6px">${kcSanitize(messagesPerField.getFirstError('password-confirm'))?no_esc}</div>
                </#if>
            </div>

            <div class="form-group">
                <#if isAppInitiatedAction??>
                    <input type="submit" value="Submit">
                    <button type="submit" name="cancel-aia" value="true" class="btn-default" style="width:100%;margin-top:8px">Cancel</button>
                <#else>
                    <input type="submit" value="Update Password">
                </#if>
            </div>
        </form>
    </#if>
</@layout.registrationLayout>
