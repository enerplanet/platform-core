<#import "template.ftl" as layout>
<@layout.registrationLayout displayInfo=true; section>
    <#if section = "form">
        <h2>Account Update Required</h2>
        <p>Please complete the following action(s):</p>

        <div style="background-color: #f9fafb; border: 1px solid #e5e7eb; border-radius: 10px; padding: 16px 20px; margin-bottom: 20px;">
            <#list requiredActions as reqAction>
                <div style="color: #111827; font-weight: 600; font-size: 15px; padding: 4px 0;">
                    ${msg("requiredAction.${reqAction}")}
                </div>
            </#list>
        </div>

        <form id="kc-update-actions-form" action="${url.loginAction}" method="post">
            <input type="submit" value="Click here to proceed">
        </form>
    </#if>
</@layout.registrationLayout>
